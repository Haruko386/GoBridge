package protocol

import (
	"encoding/binary"
	"errors"
	"fmt"
	"unicode/utf8"
)

type StreamID uint32

const (
	streamIDSize = 4

	MaxStreamDataSize    = 32 * 1024
	MaxStreamFailureSize = 1024
)

var (
	ErrInvalidStreamID        = errors.New("invalid stream ID")
	ErrMalformedStreamPayload = errors.New("malformed stream payload")
	ErrStreamDataTooLarge     = errors.New("stream data is too large")
	ErrInvalidStreamFailure   = errors.New("invalid stream failure")
)

func EncodeStreamID(id StreamID) ([]byte, error) {
	if id == 0 {
		return nil, ErrInvalidStreamID
	}

	payload := make([]byte, streamIDSize)
	binary.BigEndian.PutUint32(payload, uint32(id))
	return payload, nil
}

func DecodeStreamID(payload []byte) (StreamID, error) {
	if len(payload) != streamIDSize {
		return 0, fmt.Errorf("%w: stream ID payload length is %d, want %d", ErrMalformedStreamPayload, len(payload), streamIDSize)
	}

	id := StreamID(binary.BigEndian.Uint32(payload))
	if id == 0 {
		return 0, ErrInvalidStreamID
	}
	return id, nil
}

func EncodeStreamData(id StreamID, data []byte) ([]byte, error) {
	if id == 0 {
		return nil, ErrInvalidStreamID
	}

	if len(data) == 0 {
		return nil, fmt.Errorf("%w: stream data is empty", ErrMalformedStreamPayload)
	}

	if len(data) > MaxStreamDataSize {
		return nil, fmt.Errorf("%w: got %d bytes, maximum is %d", ErrStreamDataTooLarge, len(data), MaxStreamDataSize)
	}

	payload := make([]byte, streamIDSize+len(data))
	binary.BigEndian.PutUint32(payload, uint32(id))
	copy(payload[streamIDSize:], data)

	return payload, nil
}

func DecodeStreamData(payload []byte) (StreamID, []byte, error) {
	if len(payload) <= streamIDSize {
		return 0, nil, fmt.Errorf("%w: stream data payload length is %d, want more than %d", ErrMalformedStreamPayload, len(payload), streamIDSize)
	}

	if len(payload)-streamIDSize > MaxStreamDataSize {
		return 0, nil, fmt.Errorf("%w: got %d bytes, maximum is %d", ErrStreamDataTooLarge, len(payload)-streamIDSize, MaxStreamDataSize)
	}

	id, err := DecodeStreamID(payload[0:streamIDSize])
	if err != nil {
		return 0, nil, fmt.Errorf("decode StreamID: %w", err)
	}
	data := payload[streamIDSize:]
	return id, data, nil
}

func EncodeOpenFailed(id StreamID, reason string) ([]byte, error) {
	if id == 0 {
		return nil, ErrInvalidStreamID
	}

	if reason == "" {
		return nil, fmt.Errorf("%w: reason is empty", ErrInvalidStreamFailure)
	}

	if !utf8.ValidString(reason) {
		return nil, fmt.Errorf("%w: reason is not valid UTF-8", ErrInvalidStreamFailure)
	}

	if len(reason) > MaxStreamFailureSize {
		return nil, fmt.Errorf("%w: reason is %d bytes, maximum is %d", ErrInvalidStreamFailure, len(reason), MaxStreamFailureSize)
	}

	payload := make([]byte, streamIDSize+len(reason))
	binary.BigEndian.PutUint32(payload, uint32(id))
	copy(payload[streamIDSize:], []byte(reason))

	return payload, nil
}

func DecodeOpenFailed(payload []byte) (StreamID, string, error) {
	if len(payload) <= streamIDSize {
		return 0, "", fmt.Errorf("%w: OPEN_FAILED payload length is %d", ErrMalformedStreamPayload, len(payload))
	}

	if len(payload)-streamIDSize > MaxStreamFailureSize {
		return 0, "", fmt.Errorf("%w: reason is %d bytes, maximum is %d", ErrInvalidStreamFailure, len(payload)-streamIDSize, MaxStreamFailureSize)
	}

	id, err := DecodeStreamID(payload[0:streamIDSize])
	if err != nil {
		return 0, "", fmt.Errorf("decode StreamID: %w", err)
	}

	reason := string(payload[streamIDSize:])
	if reason == "" {
		return 0, "", fmt.Errorf("%w: reason is empty", ErrInvalidStreamFailure)
	}

	if !utf8.ValidString(reason) {
		return 0, "", fmt.Errorf("%w: reason is not valid UTF-8", ErrInvalidStreamFailure)
	}

	if len(reason) > MaxStreamFailureSize {
		return 0, "", fmt.Errorf("%w: reason is %d bytes, maximum is %d", ErrInvalidStreamFailure, len(reason), MaxStreamFailureSize)
	}

	return id, reason, nil
}
