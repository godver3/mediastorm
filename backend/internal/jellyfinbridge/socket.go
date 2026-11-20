package jellyfinbridge

import (
	"bufio"
	"context"
	"crypto/sha1"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"io"
	"log"
	"net/http"
	"strings"
	"sync"
	"time"

	"novastream/internal/jellyfinbridge/dto"
)

// handleSocket accepts Jellyfin's /socket WebSocket. It answers keep-alives and
// pushes UserDataChanged events, so favorites, watched state and resume
// positions changed on one device refresh on the profile's other devices.
// Remote control and library events are not sent. Implemented on raw
// net/http to avoid a WebSocket dependency: only text/ping/close frames from
// the client are handled. All writes go through one writer goroutine.
func (s *Server) handleSocket(w http.ResponseWriter, r *http.Request) {
	p, ok := s.authenticate(r)
	if !ok {
		log.Printf("[jellyfin-bridge] socket rejected: no valid token (query keys %q)", queryKeys(r))
		writeError(w, http.StatusUnauthorized, "Unauthorized")
		return
	}
	if !strings.EqualFold(r.Header.Get("Upgrade"), "websocket") || r.Header.Get("Sec-WebSocket-Key") == "" {
		writeError(w, http.StatusBadRequest, "WebSocket upgrade required")
		return
	}
	hj, ok := w.(http.Hijacker)
	if !ok {
		log.Printf("[jellyfin-bridge] socket rejected: response writer %T cannot hijack", w)
		writeError(w, http.StatusInternalServerError, "WebSocket unsupported")
		return
	}
	conn, rw, err := hj.Hijack()
	if err != nil {
		return
	}
	defer conn.Close()

	sum := sha1.Sum([]byte(r.Header.Get("Sec-WebSocket-Key") + "258EAFA5-E914-47DA-95CA-C5AB0DC85B11"))
	accept := base64.StdEncoding.EncodeToString(sum[:])
	_, _ = rw.WriteString("HTTP/1.1 101 Switching Protocols\r\nUpgrade: websocket\r\nConnection: Upgrade\r\nSec-WebSocket-Accept: " + accept + "\r\n\r\n")
	if rw.Flush() != nil {
		return
	}

	client := s.sockets.add(p.Profile.ID, p.Client.DeviceID, p.Session.Token, conn)
	writerDone := make(chan struct{})
	go func() {
		defer close(writerDone)
		for f := range client.out {
			_ = conn.SetWriteDeadline(time.Now().Add(10 * time.Second))
			if writeFrame(rw.Writer, f.opcode, f.payload) != nil || f.opcode == 0x8 {
				_ = conn.Close()
				return
			}
		}
	}()
	defer func() {
		s.sockets.remove(client) // closes client.out
		<-writerDone
	}()

	keepAlive, _ := json.Marshal(map[string]any{"MessageType": "ForceKeepAlive", "Data": 60, "MessageId": randomHex(16)})
	client.send(0x1, keepAlive)
	lastAuth := time.Now()
	for {
		// Keep-alives would hold a socket open indefinitely; drop it once
		// its token is signed out, revoked or expired.
		if time.Since(lastAuth) >= socketReauthInterval {
			if _, ok := s.authenticate(r); !ok {
				client.send(0x8, nil)
				return
			}
			lastAuth = time.Now()
		}
		_ = conn.SetReadDeadline(time.Now().Add(3 * time.Minute))
		opcode, payload, err := readFrame(rw.Reader)
		if err != nil {
			return
		}
		switch opcode {
		case 0x8: // close
			client.send(0x8, nil)
			return
		case 0x9: // ping
			client.send(0xA, payload)
		case 0x1: // text
			var msg struct{ MessageType string }
			if json.Unmarshal(payload, &msg) == nil && msg.MessageType == "KeepAlive" {
				reply, _ := json.Marshal(map[string]any{"MessageType": "KeepAlive", "MessageId": randomHex(16)})
				client.send(0x1, reply)
			}
		}
	}
}

type socketFrame struct {
	opcode  byte
	payload []byte
}

// socketReauthInterval is how often an open socket re-checks its token.
var socketReauthInterval = time.Minute // var for tests

type socketClient struct {
	profileID string
	deviceID  string
	// token is the session the socket signed in with: jellyfin-web's SDK
	// socket sends only the token, no deviceId, so requests of the same
	// session find it by token.
	token string
	conn  io.Closer
	out   chan socketFrame
}

// send queues a frame; a client that stops reading loses frames rather than
// blocking the sender.
func (c *socketClient) send(opcode byte, payload []byte) {
	select {
	case c.out <- socketFrame{opcode, payload}:
	default:
	}
}

// socketHub tracks open sockets per profile.
type socketHub struct {
	mu      sync.Mutex
	clients map[*socketClient]bool
}

func newSocketHub() *socketHub { return &socketHub{clients: map[*socketClient]bool{}} }

func (h *socketHub) add(profileID, deviceID, token string, conn io.Closer) *socketClient {
	c := &socketClient{profileID: profileID, deviceID: deviceID, token: token, conn: conn, out: make(chan socketFrame, 32)}
	h.mu.Lock()
	h.clients[c] = true
	h.mu.Unlock()
	return c
}

// closeAll closes every open socket's connection; each handler then sees
// its read fail and cleans up through remove.
func (h *socketHub) closeAll() {
	h.mu.Lock()
	defer h.mu.Unlock()
	for c := range h.clients {
		if c.conn != nil {
			_ = c.conn.Close()
		}
	}
}

func (h *socketHub) remove(c *socketClient) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.clients[c] {
		delete(h.clients, c)
		close(c.out)
	}
}

// reaches reports whether a socket belongs to a device of a profile: the
// same device ID, or no device ID but the same session token.
func (c *socketClient) reaches(profileID, deviceID, token string) bool {
	if c.profileID != profileID {
		return false
	}
	if deviceID == "" {
		// No device named: the caller's own session when known, else
		// every socket of the profile.
		return token == "" || c.token == token
	}
	return c.deviceID == deviceID || (c.deviceID == "" && token != "" && c.token == token)
}

// has reports whether a device of a profile has an open socket.
func (h *socketHub) has(profileID, deviceID, token string) bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	for c := range h.clients {
		if c.reaches(profileID, deviceID, token) {
			return true
		}
	}
	return false
}

// broadcast sends a message to every socket of a profile.
func (h *socketHub) broadcast(profileID, messageType string, data any) {
	msg, err := json.Marshal(map[string]any{"MessageType": messageType, "Data": data, "MessageId": randomHex(16)})
	if err != nil {
		return
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	for c := range h.clients {
		if c.profileID == profileID {
			c.send(0x1, msg)
		}
	}
}

// sendToDevice sends a message to the sockets of one device of a profile
// (every socket of the profile when the device is unknown); token finds a
// socket of the same session that did not name its device.
func (h *socketHub) sendToDevice(profileID, deviceID, token, messageType string, data any) {
	msg, err := json.Marshal(map[string]any{"MessageType": messageType, "Data": data, "MessageId": randomHex(16)})
	if err != nil {
		return
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	for c := range h.clients {
		if c.reaches(profileID, deviceID, token) {
			c.send(0x1, msg)
		}
	}
}

// notifyUserData tells the profile's sockets that an item's user data changed.
func (s *Server) notifyUserData(ctx context.Context, p *principal, ids ...string) {
	u := s.newUserState(p.Profile.ID)
	list := make([]dto.UserItemData, 0, len(ids))
	for _, id := range ids {
		list = append(list, s.userDataFor(ctx, p, u, id))
	}
	s.sockets.broadcast(p.Profile.ID, "UserDataChanged", map[string]any{"UserId": s.userID(p.Profile), "UserDataList": list})
}

const maxSocketFrame = 1 << 20

func readFrame(r *bufio.Reader) (byte, []byte, error) {
	var hdr [2]byte
	if _, err := io.ReadFull(r, hdr[:]); err != nil {
		return 0, nil, err
	}
	opcode := hdr[0] & 0x0F
	masked := hdr[1]&0x80 != 0
	length := uint64(hdr[1] & 0x7F)
	switch length {
	case 126:
		var ext [2]byte
		if _, err := io.ReadFull(r, ext[:]); err != nil {
			return 0, nil, err
		}
		length = uint64(binary.BigEndian.Uint16(ext[:]))
	case 127:
		var ext [8]byte
		if _, err := io.ReadFull(r, ext[:]); err != nil {
			return 0, nil, err
		}
		length = binary.BigEndian.Uint64(ext[:])
	}
	if length > maxSocketFrame {
		return 0, nil, io.ErrShortBuffer
	}
	var mask [4]byte
	if masked {
		if _, err := io.ReadFull(r, mask[:]); err != nil {
			return 0, nil, err
		}
	}
	payload := make([]byte, length)
	if _, err := io.ReadFull(r, payload); err != nil {
		return 0, nil, err
	}
	if masked {
		for i := range payload {
			payload[i] ^= mask[i%4]
		}
	}
	return opcode, payload, nil
}

func writeFrame(w *bufio.Writer, opcode byte, payload []byte) error {
	header := []byte{0x80 | opcode}
	switch n := len(payload); {
	case n < 126:
		header = append(header, byte(n))
	case n <= 0xFFFF:
		header = append(header, 126, byte(n>>8), byte(n))
	default:
		ext := make([]byte, 8)
		binary.BigEndian.PutUint64(ext, uint64(n))
		header = append(append(header, 127), ext...)
	}
	if _, err := w.Write(header); err != nil {
		return err
	}
	if _, err := w.Write(payload); err != nil {
		return err
	}
	return w.Flush()
}

func queryKeys(r *http.Request) []string {
	keys := make([]string, 0, len(r.URL.Query()))
	for k := range r.URL.Query() {
		keys = append(keys, k)
	}
	return keys
}
