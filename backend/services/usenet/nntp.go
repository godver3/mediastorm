package usenet

import (
	"bufio"
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"net"
	"net/textproto"
	"strconv"
	"strings"
	"time"

	"novastream/config"
)

type nntpClient struct {
	conn           net.Conn
	reader         *textproto.Reader
	writer         *textproto.Writer
	commandTimeout time.Duration
}

const defaultCommandTimeout = 15 * time.Second

func newNNTPClient(ctx context.Context, settings config.UsenetSettings) (statClient, error) {
	if strings.TrimSpace(settings.Host) == "" {
		return nil, fmt.Errorf("usenet host is required")
	}

	addr := fmt.Sprintf("%s:%d", settings.Host, settings.Port)
	dialer := &net.Dialer{}
	conn, err := dialer.DialContext(ctx, "tcp", addr)
	if err != nil {
		return nil, fmt.Errorf("dial %s: %w", addr, err)
	}

	var tlsConn *tls.Conn
	if settings.SSL {
		tlsConn = tls.Client(conn, &tls.Config{ServerName: settings.Host})
		if err := tlsConn.HandshakeContext(ctx); err != nil {
			conn.Close()
			return nil, fmt.Errorf("tls handshake: %w", err)
		}
		conn = tlsConn
	}

	reader := bufio.NewReader(conn)
	writer := bufio.NewWriter(conn)

	client := &nntpClient{
		conn:           conn,
		reader:         textproto.NewReader(reader),
		writer:         textproto.NewWriter(writer),
		commandTimeout: defaultCommandTimeout,
	}

	if err := client.initialize(ctx, settings); err != nil {
		client.conn.Close()
		return nil, err
	}

	return client, nil
}

func (c *nntpClient) initialize(ctx context.Context, settings config.UsenetSettings) error {
	code, _, err := c.readResponse(ctx)
	if err != nil {
		return fmt.Errorf("read greeting: %w", err)
	}
	if code != 200 && code != 201 {
		return fmt.Errorf("unexpected greeting status: %d", code)
	}

	if strings.TrimSpace(settings.Username) != "" {
		code, _, err := c.sendCommand(ctx, "AUTHINFO USER %s", settings.Username)
		if err != nil {
			return fmt.Errorf("auth user: %w", err)
		}

		switch code {
		case 281:
			// Authentication succeeded without password
		case 381:
			passCode, _, passErr := c.sendCommand(ctx, "AUTHINFO PASS %s", settings.Password)
			if passErr != nil {
				return fmt.Errorf("auth password: %w", passErr)
			}
			if passCode != 281 {
				return fmt.Errorf("authentication rejected with code %d", passCode)
			}
		default:
			if code >= 400 {
				return fmt.Errorf("authentication rejected with code %d", code)
			}
		}
	}

	return nil
}

func (c *nntpClient) CheckArticle(ctx context.Context, messageID string) (bool, error) {
	if strings.TrimSpace(messageID) == "" {
		return false, errors.New("empty message id")
	}

	normalizedID := strings.TrimSpace(messageID)
	if !strings.HasPrefix(normalizedID, "<") {
		normalizedID = "<" + normalizedID + ">"
	}

	// Use BODY rather than STAT. STAT only consults the provider's overview index,
	// which can still list an article (223) whose body has been purged/DMCA'd —
	// producing false-positive health checks that fail mid-playback. BODY confirms
	// the article body is actually retrievable. We drain and discard the body.
	code, _, err := c.sendCommand(ctx, "BODY %s", normalizedID)
	if err != nil {
		return false, err
	}

	switch code {
	case 222:
		// Multiline body follows; must be fully consumed to keep the connection
		// usable for the next command.
		if _, err := io.Copy(io.Discard, c.reader.DotReader()); err != nil {
			return false, fmt.Errorf("drain article body: %w", err)
		}
		return true, nil
	case 430, 411, 412:
		return false, nil
	case 503:
		return false, fmt.Errorf("server indicates bad sequence: code %d", code)
	default:
		if code >= 400 {
			return false, fmt.Errorf("nntp error response: %d", code)
		}
		return false, nil
	}
}

func (c *nntpClient) Close() error {
	if _, _, err := c.sendCommand(context.Background(), "QUIT"); err != nil {
		// Ignore errors when closing connection
	}
	return c.conn.Close()
}

func (c *nntpClient) sendCommand(ctx context.Context, format string, args ...interface{}) (int, string, error) {
	if err := c.setDeadline(ctx); err != nil {
		return 0, "", err
	}

	if err := c.writer.PrintfLine(format, args...); err != nil {
		return 0, "", err
	}
	if err := c.writer.W.Flush(); err != nil {
		return 0, "", err
	}

	return c.readResponse(ctx)
}

func (c *nntpClient) readResponse(ctx context.Context) (int, string, error) {
	if err := c.setDeadline(ctx); err != nil {
		return 0, "", err
	}

	line, err := c.reader.ReadLine()
	if err != nil {
		return 0, "", err
	}

	if len(line) < 3 {
		return 0, "", fmt.Errorf("malformed response: %q", line)
	}

	code, err := strconv.Atoi(line[:3])
	if err != nil {
		return 0, "", fmt.Errorf("invalid response code: %w", err)
	}

	message := strings.TrimSpace(line[3:])
	if strings.HasPrefix(message, "-") {
		message = strings.TrimSpace(message[1:])
	}

	return code, message, nil
}

func (c *nntpClient) setDeadline(ctx context.Context) error {
	var deadline time.Time
	if ctx != nil {
		if d, ok := ctx.Deadline(); ok {
			deadline = d
		}
	}
	if deadline.IsZero() {
		deadline = time.Now().Add(c.commandTimeout)
	}
	return c.conn.SetDeadline(deadline)
}
