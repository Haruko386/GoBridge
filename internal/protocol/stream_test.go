package protocol

import (
	"bytes"
	"errors"
	"math"
	"strings"
	"testing"
)

func TestStreamMessageTypes(t *testing.T) {
	tests := []struct {
		messageType MessageType
		name        string
	}{
		{TypeOpenStream, "OPEN_STREAM"},
		{TypeOpenOK, "OPEN_OK"},
		{TypeOpenFailed, "OPEN_FAILED"},
		{TypeStreamData, "STREAM_DATA"},
		{TypeCloseStream, "CLOSE_STREAM"},
	}

	for _, test := range tests {
		if !test.messageType.Valid() {
			t.Errorf("%s.Valid() = false", test.name)
		}
		if got := test.messageType.String(); got != test.name {
			t.Errorf("MessageType.String() = %q, want %q", got, test.name)
		}

		var wire bytes.Buffer
		if err := NewEncoder(&wire).WriteFrame(test.messageType, nil); err != nil {
			t.Fatalf("WriteFrame(%s) error = %v", test.name, err)
		}
		frame, err := NewDecoder(&wire).ReadFrame()
		if err != nil {
			t.Fatalf("ReadFrame(%s) error = %v", test.name, err)
		}
		if frame.Type != test.messageType {
			t.Errorf("round-trip type = %s, want %s", frame.Type, test.name)
		}
	}
}

func TestStreamIDRoundTrip(t *testing.T) {
	for _, want := range []StreamID{1, 42, StreamID(math.MaxUint32)} {
		payload, err := EncodeStreamID(want)
		if err != nil {
			t.Fatalf("EncodeStreamID(%d) error = %v", want, err)
		}
		if len(payload) != streamIDSize {
			t.Fatalf("EncodeStreamID(%d) length = %d, want %d", want, len(payload), streamIDSize)
		}
		got, err := DecodeStreamID(payload)
		if err != nil {
			t.Fatalf("DecodeStreamID(%d) error = %v", want, err)
		}
		if got != want {
			t.Errorf("DecodeStreamID() = %d, want %d", got, want)
		}
	}
}

func TestStreamIDRejectsInvalidValues(t *testing.T) {
	if _, err := EncodeStreamID(0); !errors.Is(err, ErrInvalidStreamID) {
		t.Fatalf("EncodeStreamID(0) error = %v, want ErrInvalidStreamID", err)
	}
	for length := 0; length <= streamIDSize+1; length++ {
		if length == streamIDSize {
			continue
		}
		if _, err := DecodeStreamID(make([]byte, length)); !errors.Is(err, ErrMalformedStreamPayload) {
			t.Errorf("DecodeStreamID(%d-byte payload) error = %v, want ErrMalformedStreamPayload", length, err)
		}
	}
	if _, err := DecodeStreamID(make([]byte, streamIDSize)); !errors.Is(err, ErrInvalidStreamID) {
		t.Fatalf("DecodeStreamID(zero ID) error = %v, want ErrInvalidStreamID", err)
	}
}

func TestStreamDataRoundTripAndCopy(t *testing.T) {
	tests := [][]byte{
		{0x01},
		[]byte("GET http://example.test/ HTTP/1.1\r\n\r\n"),
		bytes.Repeat([]byte{0xab}, MaxStreamDataSize),
	}
	for _, original := range tests {
		data := bytes.Clone(original)
		payload, err := EncodeStreamData(7, data)
		if err != nil {
			t.Fatalf("EncodeStreamData(%d bytes) error = %v", len(data), err)
		}
		data[0] ^= 0xff

		id, decoded, err := DecodeStreamData(payload)
		if err != nil {
			t.Fatalf("DecodeStreamData(%d bytes) error = %v", len(original), err)
		}
		if id != 7 {
			t.Errorf("decoded ID = %d, want 7", id)
		}
		if !bytes.Equal(decoded, original) {
			t.Errorf("decoded data differs from original; encoded payload aliased caller buffer")
		}
	}
}

func TestEncodeStreamDataRejectsInvalidInput(t *testing.T) {
	if _, err := EncodeStreamData(0, []byte("data")); !errors.Is(err, ErrInvalidStreamID) {
		t.Fatalf("EncodeStreamData(zero ID) error = %v, want ErrInvalidStreamID", err)
	}
	if _, err := EncodeStreamData(1, nil); !errors.Is(err, ErrMalformedStreamPayload) {
		t.Fatalf("EncodeStreamData(empty) error = %v, want ErrMalformedStreamPayload", err)
	}
	if _, err := EncodeStreamData(1, make([]byte, MaxStreamDataSize+1)); !errors.Is(err, ErrStreamDataTooLarge) {
		t.Fatalf("EncodeStreamData(oversized) error = %v, want ErrStreamDataTooLarge", err)
	}
}

func TestDecodeStreamDataRejectsMalformedPayloads(t *testing.T) {
	for length := 0; length <= streamIDSize; length++ {
		if _, _, err := DecodeStreamData(make([]byte, length)); !errors.Is(err, ErrMalformedStreamPayload) {
			t.Errorf("DecodeStreamData(%d-byte payload) error = %v, want ErrMalformedStreamPayload", length, err)
		}
	}

	zeroID := make([]byte, streamIDSize+1)
	zeroID[streamIDSize] = 1
	if _, _, err := DecodeStreamData(zeroID); !errors.Is(err, ErrInvalidStreamID) {
		t.Fatalf("DecodeStreamData(zero ID) error = %v, want ErrInvalidStreamID", err)
	}

	oversized := make([]byte, streamIDSize+MaxStreamDataSize+1)
	oversized[streamIDSize-1] = 1
	_, _, err := DecodeStreamData(oversized)
	if !errors.Is(err, ErrStreamDataTooLarge) {
		t.Fatalf("DecodeStreamData(oversized) error = %v, want ErrStreamDataTooLarge", err)
	}
	if strings.Contains(err.Error(), string(oversized[streamIDSize:])) {
		t.Fatal("oversized data error contains raw payload")
	}
}

func TestOpenFailedRoundTrip(t *testing.T) {
	reasons := []string{
		"connection refused",
		"代理端口不可用",
		strings.Repeat("x", MaxStreamFailureSize),
	}
	for _, reason := range reasons {
		payload, err := EncodeOpenFailed(9, reason)
		if err != nil {
			t.Fatalf("EncodeOpenFailed(%d bytes) error = %v", len(reason), err)
		}
		id, got, err := DecodeOpenFailed(payload)
		if err != nil {
			t.Fatalf("DecodeOpenFailed(%d bytes) error = %v", len(reason), err)
		}
		if id != 9 || got != reason {
			t.Errorf("DecodeOpenFailed() = (%d, %q), want (9, %q)", id, got, reason)
		}
	}
}

func TestEncodeOpenFailedRejectsInvalidInput(t *testing.T) {
	tests := []struct {
		name   string
		id     StreamID
		reason string
		want   error
	}{
		{name: "zero ID", reason: "failed", want: ErrInvalidStreamID},
		{name: "empty reason", id: 1, want: ErrInvalidStreamFailure},
		{name: "invalid UTF-8", id: 1, reason: string([]byte{0xff}), want: ErrInvalidStreamFailure},
		{name: "oversized reason", id: 1, reason: strings.Repeat("x", MaxStreamFailureSize+1), want: ErrInvalidStreamFailure},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if _, err := EncodeOpenFailed(test.id, test.reason); !errors.Is(err, test.want) {
				t.Fatalf("EncodeOpenFailed() error = %v, want %v", err, test.want)
			}
		})
	}
}

func TestDecodeOpenFailedRejectsMalformedPayloads(t *testing.T) {
	for length := 0; length <= streamIDSize; length++ {
		if _, _, err := DecodeOpenFailed(make([]byte, length)); !errors.Is(err, ErrMalformedStreamPayload) {
			t.Errorf("DecodeOpenFailed(%d-byte payload) error = %v, want ErrMalformedStreamPayload", length, err)
		}
	}

	zeroID := append(make([]byte, streamIDSize), []byte("failed")...)
	if _, _, err := DecodeOpenFailed(zeroID); !errors.Is(err, ErrInvalidStreamID) {
		t.Fatalf("DecodeOpenFailed(zero ID) error = %v, want ErrInvalidStreamID", err)
	}

	invalidUTF8, err := EncodeStreamID(1)
	if err != nil {
		t.Fatalf("EncodeStreamID() error = %v", err)
	}
	invalidUTF8 = append(invalidUTF8, 0xff)
	if _, _, err := DecodeOpenFailed(invalidUTF8); !errors.Is(err, ErrInvalidStreamFailure) {
		t.Fatalf("DecodeOpenFailed(invalid UTF-8) error = %v, want ErrInvalidStreamFailure", err)
	}

	oversized, err := EncodeStreamID(1)
	if err != nil {
		t.Fatalf("EncodeStreamID() error = %v", err)
	}
	oversized = append(oversized, bytes.Repeat([]byte("secret"), MaxStreamFailureSize/6+2)...)
	_, _, err = DecodeOpenFailed(oversized)
	if !errors.Is(err, ErrInvalidStreamFailure) {
		t.Fatalf("DecodeOpenFailed(oversized) error = %v, want ErrInvalidStreamFailure", err)
	}
	if strings.Contains(err.Error(), "secret") {
		t.Fatal("oversized failure error contains raw reason")
	}
}
