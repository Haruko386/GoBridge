package protocol

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
	"sync"
	"testing"
	"testing/iotest"
	"time"
)

func TestFrameRoundTrip(t *testing.T) {
	var transport bytes.Buffer

	encoder := NewEncoder(&transport)
	decoder := NewDecoder(&transport)

	wantFrames := []Frame{
		{
			Type:    TypePing,
			Payload: nil,
		},
		{
			Type:    TypeAuthChallenge,
			Payload: []byte("challenge"),
		},
		{
			Type:    TypeAuthResponse,
			Payload: []byte("signature"),
		},
	}

	for _, frame := range wantFrames {
		if err := encoder.WriteFrame(
			frame.Type,
			frame.Payload,
		); err != nil {
			t.Fatalf("WriteFrame() error = %v", err)
		}
	}

	for index, want := range wantFrames {
		got, err := decoder.ReadFrame()
		if err != nil {
			t.Fatalf(
				"ReadFrame(%d) error = %v",
				index,
				err,
			)
		}

		if got.Type != want.Type {
			t.Errorf(
				"frame %d type = %v, want %v",
				index,
				got.Type,
				want.Type,
			)
		}

		if !bytes.Equal(got.Payload, want.Payload) {
			t.Errorf(
				"frame %d payload = %q, want %q",
				index,
				got.Payload,
				want.Payload,
			)
		}
	}
}

func TestDecoderHandlesFragmentedReads(t *testing.T) {
	var encoded bytes.Buffer

	encoder := NewEncoder(&encoded)
	if err := encoder.WriteFrame(
		TypeAuthChallenge,
		[]byte("fragmented payload"),
	); err != nil {
		t.Fatalf("WriteFrame() error = %v", err)
	}

	oneByteAtATime := iotest.OneByteReader(
		bytes.NewReader(encoded.Bytes()),
	)

	frame, err := NewDecoder(oneByteAtATime).ReadFrame()
	if err != nil {
		t.Fatalf("ReadFrame() error = %v", err)
	}

	if frame.Type != TypeAuthChallenge {
		t.Errorf(
			"frame type = %v, want %v",
			frame.Type,
			TypeAuthChallenge,
		)
	}

	if string(frame.Payload) != "fragmented payload" {
		t.Errorf(
			"frame payload = %q",
			frame.Payload,
		)
	}
}

func TestEncoderHandlesShortWrites(t *testing.T) {
	var destination bytes.Buffer

	writer := &limitedWriter{
		writer: &destination,
		limit:  1,
	}

	encoder := NewEncoder(writer)

	if err := encoder.WriteFrame(
		TypePing,
		[]byte("payload"),
	); err != nil {
		t.Fatalf("WriteFrame() error = %v", err)
	}

	frame, err := NewDecoder(&destination).ReadFrame()
	if err != nil {
		t.Fatalf("ReadFrame() error = %v", err)
	}

	if string(frame.Payload) != "payload" {
		t.Errorf(
			"frame payload = %q, want %q",
			frame.Payload,
			"payload",
		)
	}
}

func TestEncoderRejectsInvalidFrame(t *testing.T) {
	tests := []struct {
		name        string
		messageType MessageType
		payload     []byte
		wantError   error
	}{
		{
			name:        "unknown message type",
			messageType: 255,
			wantError:   ErrUnknownType,
		},
		{
			name:        "payload too large",
			messageType: TypePing,
			payload:     make([]byte, MaxPayloadSize+1),
			wantError:   ErrPayloadTooLarge,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := NewEncoder(io.Discard).WriteFrame(
				tt.messageType,
				tt.payload,
			)

			if !errors.Is(err, tt.wantError) {
				t.Fatalf(
					"WriteFrame() error = %v, want %v",
					err,
					tt.wantError,
				)
			}
		})
	}
}

func TestDecoderRejectsInvalidHeader(t *testing.T) {
	tests := []struct {
		name      string
		header    []byte
		wantError error
	}{
		{
			name: "invalid magic",
			header: makeHeader(
				[4]byte{'B', 'A', 'D', '!'},
				TypePing,
				0,
			),
			wantError: ErrInvalidMagic,
		},
		{
			name: "unknown message type",
			header: makeHeader(
				magic,
				MessageType(255),
				0,
			),
			wantError: ErrUnknownType,
		},
		{
			name: "payload too large",
			header: makeHeader(
				magic,
				TypePing,
				MaxPayloadSize+1,
			),
			wantError: ErrPayloadTooLarge,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := NewDecoder(
				bytes.NewReader(tt.header),
			).ReadFrame()

			if !errors.Is(err, tt.wantError) {
				t.Fatalf(
					"ReadFrame() error = %v, want %v",
					err,
					tt.wantError,
				)
			}
		})
	}
}

func TestDecoderRejectsTruncatedFrame(t *testing.T) {
	tests := []struct {
		name string
		data []byte
	}{
		{
			name: "truncated header",
			data: []byte{'G', 'B'},
		},
		{
			name: "missing payload",
			data: append(
				makeHeader(
					magic,
					TypePing,
					10,
				),
				[]byte("short")...,
			),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := NewDecoder(
				bytes.NewReader(tt.data),
			).ReadFrame()

			if !errors.Is(err, io.ErrUnexpectedEOF) {
				t.Fatalf(
					"ReadFrame() error = %v, want io.ErrUnexpectedEOF",
					err,
				)
			}
		})
	}
}

func TestConcurrentWritesDoNotInterleave(t *testing.T) {
	const frameCount = 50

	writerConn, readerConn := net.Pipe()
	defer writerConn.Close()
	defer readerConn.Close()

	deadline := time.Now().Add(5 * time.Second)

	if err := writerConn.SetDeadline(deadline); err != nil {
		t.Fatalf("writer SetDeadline() error = %v", err)
	}
	if err := readerConn.SetDeadline(deadline); err != nil {
		t.Fatalf("reader SetDeadline() error = %v", err)
	}

	encoder := NewEncoder(writerConn)
	decoder := NewDecoder(readerConn)

	var waitGroup sync.WaitGroup
	writeErrors := make(chan error, frameCount)

	for index := range frameCount {
		payload := []byte(
			fmt.Sprintf("message-%03d", index),
		)

		waitGroup.Add(1)

		go func() {
			defer waitGroup.Done()

			writeErrors <- encoder.WriteFrame(
				TypePing,
				payload,
			)
		}()
	}

	received := make(map[string]bool, frameCount)

	for range frameCount {
		frame, err := decoder.ReadFrame()
		if err != nil {
			t.Fatalf("ReadFrame() error = %v", err)
		}

		if frame.Type != TypePing {
			t.Fatalf(
				"frame type = %v, want %v",
				frame.Type,
				TypePing,
			)
		}

		received[string(frame.Payload)] = true
	}

	waitGroup.Wait()
	close(writeErrors)

	for err := range writeErrors {
		if err != nil {
			t.Fatalf("WriteFrame() error = %v", err)
		}
	}

	if len(received) != frameCount {
		t.Errorf(
			"received %d unique frames, want %d",
			len(received),
			frameCount,
		)
	}
}

func TestMessageTypeString(t *testing.T) {
	tests := []struct {
		messageType MessageType
		want        string
	}{
		{messageType: TypePing, want: "PING"},
		{messageType: TypePong, want: "PONG"},
		{messageType: 255, want: "UNKNOWN(255)"},
	}

	for _, tt := range tests {
		if got := tt.messageType.String(); got != tt.want {
			t.Errorf(
				"MessageType(%d).String() = %q, want %q",
				tt.messageType,
				got,
				tt.want,
			)
		}
	}
}

type limitedWriter struct {
	writer io.Writer
	limit  int
}

func (w *limitedWriter) Write(data []byte) (int, error) {
	if len(data) > w.limit {
		data = data[:w.limit]
	}

	return w.writer.Write(data)
}

func makeHeader(
	frameMagic [4]byte,
	messageType MessageType,
	payloadSize uint32,
) []byte {
	header := make([]byte, HeaderSize)

	copy(header[0:4], frameMagic[:])
	header[4] = byte(messageType)

	binary.BigEndian.PutUint32(
		header[5:9],
		payloadSize,
	)

	return header
}
