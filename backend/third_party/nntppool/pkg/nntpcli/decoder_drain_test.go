package nntpcli

import (
	"bufio"
	"bytes"
	"io"
	"net"
	"strings"
	"testing"
	"time"
)

// yencEncodeLine encodes raw bytes as a single yEnc line (no line wrapping).
func yencEncodeLine(raw []byte) string {
	var sb strings.Builder
	for _, b := range raw {
		e := b + 42
		switch e {
		case 0x00, 0x0A, 0x0D, '=', '.':
			sb.WriteByte('=')
			e += 64
		}
		sb.WriteByte(e)
	}
	return sb.String()
}

// TestBodyDecodedConsumesYencTrailer reproduces the connection desync seen in
// production: the decoder stopped at "\r\n=y" (EndControl) and left the rest of
// the "=yend ..." line plus the ".\r\n" terminator on the connection, so the
// next command on the reused connection read "end size=..." as its status line.
func TestBodyDecodedConsumesYencTrailer(t *testing.T) {
	for _, tc := range []struct {
		name       string
		splitTrail bool // deliver the =yend trailer in a separate, delayed write
	}{
		{name: "trailer in same packet", splitTrail: false},
		{name: "trailer arrives later", splitTrail: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			client, server := net.Pipe()
			defer client.Close()
			defer server.Close()

			payload := bytes.Repeat([]byte("strmr-payload-"), 50)
			encoded := yencEncodeLine(payload)
			head := "222 0 <a@b> body\r\n" +
				"=ybegin part=1 line=128 size=700 name=x.mkv\r\n" +
				"=ypart begin=1 end=700\r\n" +
				encoded + "\r\n=y"
			tail := "end size=700 part=1 pcrc32=deadbeef\r\n.\r\n"

			serverErr := make(chan error, 1)
			go func() {
				br := bufio.NewReader(server)
				write := func(s string) bool {
					_, err := io.WriteString(server, s)
					if err != nil {
						serverErr <- err
						return false
					}
					return true
				}
				if !write("200 hello\r\n") {
					return
				}
				// First BODY.
				if _, err := br.ReadString('\n'); err != nil {
					serverErr <- err
					return
				}
				if tc.splitTrail {
					if !write(head) {
						return
					}
					time.Sleep(50 * time.Millisecond)
					if !write(tail) {
						return
					}
				} else if !write(head + tail) {
					return
				}
				// Second command must start on a clean line.
				line, err := br.ReadString('\n')
				if err != nil {
					serverErr <- err
					return
				}
				if !strings.HasPrefix(line, "STAT") {
					serverErr <- io.ErrUnexpectedEOF
					return
				}
				serverErr <- nil
				write("223 1 <a@b>\r\n")
			}()

			conn, err := newConnection(client, time.Now().Add(time.Hour), 5*time.Second)
			if err != nil {
				t.Fatalf("newConnection: %v", err)
			}

			var out bytes.Buffer
			n, err := conn.BodyDecoded("a@b", &out, 0)
			if err != nil {
				t.Fatalf("BodyDecoded: %v", err)
			}
			if n != int64(len(payload)) || !bytes.Equal(out.Bytes(), payload) {
				t.Fatalf("decoded %d bytes, want %d", n, len(payload))
			}

			if _, err := conn.Stat("a@b"); err != nil {
				t.Fatalf("next command on reused connection failed (desync): %v", err)
			}
			if err := <-serverErr; err != nil {
				t.Fatalf("server: %v", err)
			}
		})
	}
}
