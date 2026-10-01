// Package protocol implements GoBridge's framed wire protocol.
package protocol

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"sync"
)

const (
	HeaderSize     = 9
	MaxPayloadSize = 1 << 20 // 1MB
)

var (
	magic = [4]byte{'G', 'B', 'R', '1'}

	ErrInvalidMagic    = errors.New("invalid protocol magic")
	ErrUnknownType     = errors.New("unknown message type")
	ErrPayloadTooLarge = errors.New("frame payload is too large")
)

type MessageType uint8

const (
	TypeAuthChallenge MessageType = 1
	TypeAuthResponse  MessageType = 2
	TypeAuthOK        MessageType = 3
	TypeAuthFailed    MessageType = 4
	TypePing          MessageType = 5
	TypePong          MessageType = 6
	TypePairChallenge MessageType = 7
	TypePairRequest   MessageType = 8
	TypePairOK        MessageType = 9
	TypePairFailed    MessageType = 10
	TypeOpenStream    MessageType = 11
	TypeOpenOK        MessageType = 12
	TypeOpenFailed    MessageType = 13
	TypeStreamData    MessageType = 14
	TypeCloseStream   MessageType = 15
)

type Frame struct {
	Type    MessageType
	Payload []byte
}

func (t MessageType) Valid() bool {
	switch t {
	case TypeAuthChallenge, TypeAuthResponse, TypeAuthOK, TypeAuthFailed, TypePing, TypePong, TypePairChallenge, TypePairRequest, TypePairOK, TypePairFailed, TypeOpenStream, TypeOpenOK, TypeOpenFailed, TypeStreamData, TypeCloseStream:
		return true
	default:
		return false
	}
}

func (t MessageType) String() string {
	switch t {
	case TypeAuthChallenge:
		return "AUTH_CHALLENGE"
	case TypeAuthResponse:
		return "AUTH_RESPONSE"
	case TypeAuthOK:
		return "AUTH_OK"
	case TypeAuthFailed:
		return "AUTH_FAILED"
	case TypePing:
		return "PING"
	case TypePong:
		return "PONG"
	case TypePairChallenge:
		return "PAIR_CHALLENGE"
	case TypePairRequest:
		return "PAIR_REQUEST"
	case TypePairOK:
		return "PAIR_OK"
	case TypePairFailed:
		return "PAIR_FAILED"
	case TypeOpenStream:
		return "OPEN_STREAM"
	case TypeOpenOK:
		return "OPEN_OK"
	case TypeOpenFailed:
		return "OPEN_FAILED"
	case TypeStreamData:
		return "STREAM_DATA"
	case TypeCloseStream:
		return "CLOSE_STREAM"
	default:
		return fmt.Sprintf("UNKNOWN(%d)", uint8(t))
	}
}

type Encoder struct {
	writer io.Writer

	// Multiple goroutines may send frames through one connection.
	// The mutex prevents one frame's header and payload from being
	// interleaved with another frame.
	mu sync.Mutex
}

func NewEncoder(w io.Writer) *Encoder {
	return &Encoder{writer: w}
}

func (e *Encoder) WriteFrame(messageType MessageType, payload []byte) error {
	if !messageType.Valid() {
		return fmt.Errorf("%w: %d", ErrUnknownType, messageType)
	}

	if len(payload) > MaxPayloadSize {
		return fmt.Errorf("%w: %d bytes, maximum is %d", ErrPayloadTooLarge, len(payload), MaxPayloadSize)
	}

	// write header
	var header [HeaderSize]byte
	copy(header[:4], magic[:])
	// write message type
	header[4] = byte(messageType)
	// write payload
	binary.BigEndian.PutUint32(header[5:9], uint32(len(payload)))

	e.mu.Lock()
	defer e.mu.Unlock()

	if err := writeFull(e.writer, header[:]); err != nil {
		return fmt.Errorf("write frame header: %w", err)
	}

	if len(payload) == 0 {
		return nil
	}

	if err := writeFull(e.writer, payload); err != nil {
		return fmt.Errorf("write frame payload: %w", err)
	}

	return nil
}

/* Decoder */
type Decoder struct {
	reader io.Reader
}

func NewDecoder(r io.Reader) *Decoder {
	return &Decoder{reader: r}
}

func (d *Decoder) ReadFrame() (Frame, error) {
	var header [HeaderSize]byte

	if _, err := io.ReadFull(d.reader, header[:]); err != nil {
		return Frame{}, fmt.Errorf("read frame header: %w", err)
	}

	if !bytes.Equal(header[:4], magic[:]) {
		return Frame{}, ErrInvalidMagic
	}

	messageType := MessageType(header[4])
	if !messageType.Valid() {
		return Frame{}, fmt.Errorf("%w: %d", ErrUnknownType, messageType)
	}

	payloadSize := binary.BigEndian.Uint32(header[5:9])
	if payloadSize > MaxPayloadSize {
		return Frame{}, fmt.Errorf("%w: %d bytes, maximum is %d", ErrPayloadTooLarge, payloadSize, MaxPayloadSize)
	}

	payload := make([]byte, payloadSize)
	if payloadSize > 0 {
		if _, err := io.ReadFull(d.reader, payload[:]); err != nil {
			return Frame{}, fmt.Errorf("read frame payload: %w", err)
		}
	}

	return Frame{
		Type:    messageType,
		Payload: payload,
	}, nil
}

func writeFull(writer io.Writer, data []byte) error {
	for len(data) > 0 {
		written, err := writer.Write(data)

		if written < 0 || written > len(data) {
			return fmt.Errorf("invalid write count %d for %d bytes", written, len(data))
		}

		if written > 0 {
			data = data[written:]
		}

		if err != nil {
			return err
		}

		if written == 0 {
			return io.ErrShortWrite
		}
	}
	return nil
}
