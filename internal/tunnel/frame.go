package tunnel

import (
	"encoding/binary"
	"fmt"
	"io"
)

const (
	maxFrameSize = 65535
	headerSize   = 4
)

// WriteFrame writes a framed message to w.
// Format: [4 bytes big-endian: 1+len(payload)][1 byte: msgType][payload...]
func WriteFrame(w io.Writer, msgType byte, payload []byte) error {
	contentLen := 1 + len(payload)
	if contentLen > maxFrameSize {
		return fmt.Errorf("frame too large: %d bytes", contentLen)
	}

	buf := make([]byte, headerSize+contentLen)
	binary.BigEndian.PutUint32(buf[0:4], uint32(contentLen))
	buf[4] = msgType
	copy(buf[5:], payload)

	_, err := w.Write(buf)
	return err
}

// ReadFrame reads one framed message from r.
// Returns the message type and the payload (without the type byte).
func ReadFrame(r io.Reader) (msgType byte, payload []byte, err error) {
	header := make([]byte, headerSize)
	if _, err = io.ReadFull(r, header); err != nil {
		return
	}

	contentLen := binary.BigEndian.Uint32(header)
	if contentLen == 0 || contentLen > maxFrameSize {
		err = fmt.Errorf("invalid frame content length: %d", contentLen)
		return
	}

	content := make([]byte, contentLen)
	if _, err = io.ReadFull(r, content); err != nil {
		return
	}

	msgType = content[0]
	payload = content[1:]
	return
}
