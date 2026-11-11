package nntpcli

import (
	"bufio"
	"bytes"
	"errors"
	"io"
	"strings"

	"github.com/mnightingale/rapidyenc"
)

const (
	// decoderBufferSize is the buffer size for incremental decoding.
	decoderBufferSize = 32 * 1024
)

// incrementalDecoder wraps rapidyenc.DecodeIncremental to provide an io.Reader interface.
// This decoder handles yenc headers (=ybegin, =ypart) by skipping them, then uses
// DecodeIncremental for the actual encoded body data.
type incrementalDecoder struct {
	r           *bufio.Reader
	state       rapidyenc.State
	buf         []byte // read buffer for body data
	bufStart    int    // start of unconsumed data in buf
	bufEnd      int    // end of data in buf
	decoded     []byte // decoded data not yet consumed by caller
	headersRead bool   // true after headers have been parsed/skipped
	eof         bool   // true when end of yenc data reached
	readErr     error  // stored read error
	drainErr    error  // failure consuming the article trailer; connection is unusable
}

// newIncrementalDecoder creates a new incremental yenc decoder.
func newIncrementalDecoder(r io.Reader) *incrementalDecoder {
	return &incrementalDecoder{
		r:   bufio.NewReaderSize(r, decoderBufferSize),
		buf: make([]byte, decoderBufferSize),
	}
}

// skipHeaders reads and skips the yenc header lines (=ybegin, =ypart).
// After this, the reader is positioned at the start of the encoded body.
func (d *incrementalDecoder) skipHeaders() error {
	for {
		line, err := d.r.ReadString('\n')
		if err != nil {
			return err
		}

		// Trim the line for comparison
		trimmed := strings.TrimSpace(line)

		if strings.HasPrefix(trimmed, "=ybegin") {
			// Found =ybegin, continue to check for =ypart
			continue
		}

		if strings.HasPrefix(trimmed, "=ypart") {
			// Found =ypart, body starts after this
			continue
		}

		// This line is not a header - it's the start of the body
		// Put this data back into our buffer
		d.bufEnd = copy(d.buf, []byte(line))
		break
	}

	d.headersRead = true
	return nil
}

// Read implements io.Reader. It reads yenc-encoded data from the underlying
// reader and returns decoded data.
func (d *incrementalDecoder) Read(p []byte) (int, error) {
	// Skip headers on first read
	if !d.headersRead {
		if err := d.skipHeaders(); err != nil {
			return 0, err
		}
	}

	// If we have leftover decoded data, return that first
	if len(d.decoded) > 0 {
		n := copy(p, d.decoded)
		d.decoded = d.decoded[n:]
		return n, nil
	}

	// If we've reached EOF, return EOF
	if d.eof {
		return 0, d.endErr()
	}

	// Try to fill the buffer if we have space
	if d.bufEnd < len(d.buf) && d.readErr == nil {
		n, err := d.r.Read(d.buf[d.bufEnd:])
		d.bufEnd += n
		if err != nil {
			d.readErr = err
		}
	}

	// If we have no data to process, return
	if d.bufStart >= d.bufEnd {
		if d.readErr != nil {
			return 0, d.readErr
		}
		return 0, io.EOF
	}

	// Get the data to decode
	input := d.buf[d.bufStart:d.bufEnd]

	// Create output buffer - decode into a separate buffer to avoid overwrites
	output := make([]byte, len(input))

	// Decode
	nDst, nSrc, end, decErr := rapidyenc.DecodeIncremental(output, input, &d.state)
	if decErr != nil {
		return 0, decErr
	}

	// Advance the buffer start by consumed bytes
	d.bufStart += nSrc

	// If we've consumed all data, reset the buffer positions
	if d.bufStart >= d.bufEnd {
		d.bufStart = 0
		d.bufEnd = 0
	} else if d.bufStart > len(d.buf)/2 {
		// Compact the buffer if we've consumed more than half
		copy(d.buf, d.buf[d.bufStart:d.bufEnd])
		d.bufEnd -= d.bufStart
		d.bufStart = 0
	}

	// Check if we reached the end
	if end != rapidyenc.EndNone {
		d.eof = true
		// EndControl stops right after "\r\n=y": the rest of the =yend line
		// and the ".\r\n" terminator are still unread (possibly not yet even
		// received). Consume them now, or the next command on this reused
		// connection reads "end size=..." as its status line.
		if end == rapidyenc.EndControl {
			if err := d.drainToTerminator(); err != nil {
				d.drainErr = err
			}
		}
	}

	// Return decoded data
	if nDst > 0 {
		copied := copy(p, output[:nDst])
		if copied < nDst {
			// Save the rest for next Read call
			d.decoded = make([]byte, nDst-copied)
			copy(d.decoded, output[copied:nDst])
		}
		return copied, nil
	}

	// If no data was decoded but we haven't reached EOF, try again
	if !d.eof && d.readErr == nil {
		return d.Read(p)
	}

	if d.eof {
		return 0, d.endErr()
	}

	return 0, d.readErr
}

// drainToTerminator consumes the remainder of the article, through the
// "\r\n.\r\n" terminator, after the decoder stopped at a yEnc control line.
// Unconsumed bytes still in d.buf are scanned first, then the underlying reader.
func (d *incrementalDecoder) drainToTerminator() error {
	pending := append([]byte(nil), d.buf[d.bufStart:d.bufEnd]...)
	d.bufStart, d.bufEnd = 0, 0

	// The decoder stopped mid-line (after "=y"), so the first line is never the
	// terminator.
	atLineStart := false
	for {
		var line []byte
		complete := false
		if i := bytes.IndexByte(pending, '\n'); i >= 0 {
			line = pending[:i+1]
			pending = pending[i+1:]
			complete = true
		} else {
			chunk, err := d.r.ReadSlice('\n')
			line = append(pending, chunk...)
			pending = nil
			switch {
			case err == nil:
				complete = true
			case errors.Is(err, bufio.ErrBufferFull):
				// Overlong line: keep scanning; it cannot be the terminator.
			default:
				return err
			}
		}

		if atLineStart && complete && (bytes.Equal(line, []byte(".\r\n")) || bytes.Equal(line, []byte(".\n"))) {
			return nil
		}
		atLineStart = complete
	}
}

// endErr is io.EOF unless the article trailer could not be consumed, in which
// case the error is surfaced so the caller discards the connection.
func (d *incrementalDecoder) endErr() error {
	if d.drainErr != nil {
		return d.drainErr
	}
	return io.EOF
}
