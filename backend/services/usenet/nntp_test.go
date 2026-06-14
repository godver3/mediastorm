package usenet

import (
	"bufio"
	"context"
	"fmt"
	"net"
	"net/textproto"
	"strings"
	"testing"
	"time"

	"novastream/config"
)

func TestNNTPClientCheckArticle(t *testing.T) {
	serverConn, clientConn := net.Pipe()
	t.Cleanup(func() {
		serverConn.Close()
		clientConn.Close()
	})

	go func() {
		writer := bufio.NewWriter(serverConn)
		reader := bufio.NewReader(serverConn)

		fmt.Fprintf(writer, "200 server ready\r\n")
		writer.Flush()

		for {
			line, err := reader.ReadString('\n')
			if err != nil {
				return
			}
			cmd := strings.TrimSpace(line)
			switch {
			case strings.HasPrefix(cmd, "AUTHINFO USER"):
				fmt.Fprintf(writer, "281 ok\r\n")
			case strings.HasPrefix(cmd, "BODY "):
				id := strings.TrimSpace(cmd[5:])
				if id == "<item1@test>" || id == "<bare@test>" {
					// Article body present: 222 + dot-terminated multiline body.
					fmt.Fprintf(writer, "222 0 %s body follows\r\n", id)
					fmt.Fprintf(writer, "=ybegin line=128 size=8 name=x\r\n")
					fmt.Fprintf(writer, "payloaddata\r\n")
					fmt.Fprintf(writer, ".\r\n")
				} else {
					// Includes the case where STAT would have returned 223 but the
					// body has been purged — BODY reports it missing.
					fmt.Fprintf(writer, "430 no such article\r\n")
				}
			case cmd == "QUIT":
				fmt.Fprintf(writer, "205 closing connection\r\n")
				writer.Flush()
				return
			default:
				fmt.Fprintf(writer, "500 command not supported\r\n")
			}
			writer.Flush()
		}
	}()

	client := &nntpClient{
		conn:           clientConn,
		reader:         textproto.NewReader(bufio.NewReader(clientConn)),
		writer:         textproto.NewWriter(bufio.NewWriter(clientConn)),
		commandTimeout: time.Second,
	}

	ctx := context.Background()
	if err := client.initialize(ctx, config.UsenetSettings{Username: "user"}); err != nil {
		t.Fatalf("initialize: %v", err)
	}

	ok, err := client.CheckArticle(ctx, "<item1@test>")
	if err != nil {
		t.Fatalf("CheckArticle returned error: %v", err)
	}
	if !ok {
		t.Fatalf("expected article to be present")
	}

	missing, err := client.CheckArticle(ctx, "<missing@test>")
	if err != nil {
		t.Fatalf("CheckArticle missing returned error: %v", err)
	}
	if missing {
		t.Fatalf("expected missing article to return false")
	}

	okBare, err := client.CheckArticle(ctx, "bare@test")
	if err != nil {
		t.Fatalf("CheckArticle bare id error: %v", err)
	}
	if !okBare {
		t.Fatalf("expected bare id lookup to succeed")
	}

	if err := client.Close(); err != nil {
		t.Fatalf("Close returned error: %v", err)
	}
}
