package pairing

import (
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/binary"
	"errors"
	"strings"
	"testing"
)

func TestRequestRoundTrip(t *testing.T) {
	publicKey, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("GenerateKey() error = %v", err)
	}
	signature := ed25519.Sign(privateKey, []byte("challenge"))
	want := request{
		name:      "实验室-client",
		publicKey: publicKey,
		signature: signature,
		code:      "GBP1.public.secret",
	}

	payload, err := encodeRequest(want)
	if err != nil {
		t.Fatalf("encodeRequest() error = %v", err)
	}
	got, err := decodeRequest(payload)
	if err != nil {
		t.Fatalf("decodeRequest() error = %v", err)
	}
	if got.name != want.name || got.code != want.code {
		t.Fatalf("decoded text = {%q, %q}, want {%q, %q}", got.name, got.code, want.name, want.code)
	}
	if !bytes.Equal(got.publicKey, want.publicKey) {
		t.Fatal("decoded public key does not match")
	}
	if !bytes.Equal(got.signature, want.signature) {
		t.Fatal("decoded signature does not match")
	}

	// Decoded byte slices must not alias the caller-owned payload.
	payload[0] ^= 0xff
	if !bytes.Equal(got.publicKey, want.publicKey) {
		t.Fatal("mutating payload changed decoded public key")
	}
}

func TestResponseRoundTrip(t *testing.T) {
	publicKey := testPublicKey(t)
	want := response{name: "machine-room-server", publicKey: publicKey}

	payload, err := encodeResponse(want)
	if err != nil {
		t.Fatalf("encodeResponse() error = %v", err)
	}
	got, err := decodeResponse(payload)
	if err != nil {
		t.Fatalf("decodeResponse() error = %v", err)
	}
	if got.name != want.name || !bytes.Equal(got.publicKey, want.publicKey) {
		t.Fatalf("decodeResponse() = %#v, want %#v", got, want)
	}

	payload[0] ^= 0xff
	if !bytes.Equal(got.publicKey, want.publicKey) {
		t.Fatal("mutating payload changed decoded response public key")
	}
}

func TestMessageEncodersRejectInvalidValues(t *testing.T) {
	publicKey := testPublicKey(t)
	signature := make([]byte, ed25519.SignatureSize)
	tests := []struct {
		name string
		run  func() error
	}{
		{"empty request name", func() error {
			_, err := encodeRequest(request{publicKey: publicKey, signature: signature, code: "code"})
			return err
		}},
		{"surrounding whitespace", func() error {
			_, err := encodeRequest(request{name: " alice ", publicKey: publicKey, signature: signature, code: "code"})
			return err
		}},
		{"invalid UTF-8", func() error {
			_, err := encodeRequest(request{name: string([]byte{0xff}), publicKey: publicKey, signature: signature, code: "code"})
			return err
		}},
		{"long name", func() error {
			_, err := encodeRequest(request{name: strings.Repeat("a", MaxPeerNameSize+1), publicKey: publicKey, signature: signature, code: "code"})
			return err
		}},
		{"short public key", func() error {
			_, err := encodeRequest(request{name: "alice", publicKey: publicKey[:1], signature: signature, code: "code"})
			return err
		}},
		{"short signature", func() error {
			_, err := encodeRequest(request{name: "alice", publicKey: publicKey, signature: signature[:1], code: "code"})
			return err
		}},
		{"empty code", func() error {
			_, err := encodeRequest(request{name: "alice", publicKey: publicKey, signature: signature})
			return err
		}},
		{"long code", func() error {
			_, err := encodeRequest(request{name: "alice", publicKey: publicKey, signature: signature, code: strings.Repeat("a", MaxCodeSize+1)})
			return err
		}},
		{"invalid response name", func() error { _, err := encodeResponse(response{publicKey: publicKey}); return err }},
		{"invalid response key", func() error { _, err := encodeResponse(response{name: "server", publicKey: publicKey[:1]}); return err }},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if err := tt.run(); err == nil {
				t.Fatal("error = nil, want validation error")
			}
		})
	}
}

func TestDecodeRequestRejectsMalformedPayloadWithoutPanic(t *testing.T) {
	validPayload := validRequestPayload(t)

	declaredNameTooLong := bytes.Clone(validPayload[:pairRequestFixedSize])
	binary.BigEndian.PutUint16(
		declaredNameTooLong[ed25519.PublicKeySize+ed25519.SignatureSize:],
		MaxPeerNameSize+1,
	)

	declaredCodeTooLong := bytes.Clone(validPayload[:pairRequestFixedSize])
	binary.BigEndian.PutUint16(
		declaredCodeTooLong[ed25519.PublicKeySize+ed25519.SignatureSize+2:],
		MaxCodeSize+1,
	)

	truncated := bytes.Clone(validPayload[:pairRequestFixedSize])
	binary.BigEndian.PutUint16(
		truncated[ed25519.PublicKeySize+ed25519.SignatureSize:],
		10,
	)

	tests := [][]byte{
		nil,
		validPayload[:pairRequestFixedSize-1],
		declaredNameTooLong,
		declaredCodeTooLong,
		truncated,
		append(bytes.Clone(validPayload), 0),
	}

	for i, payload := range tests {
		t.Run(string(rune('A'+i)), func(t *testing.T) {
			defer func() {
				if recovered := recover(); recovered != nil {
					t.Fatalf("decodeRequest() panicked: %v", recovered)
				}
			}()
			if _, err := decodeRequest(payload); !errors.Is(err, ErrMalformedMessage) {
				t.Fatalf("decodeRequest() error = %v, want ErrMalformedMessage", err)
			}
		})
	}
}

func TestDecodeResponseRejectsMalformedPayload(t *testing.T) {
	validPayload, err := encodeResponse(response{name: "server", publicKey: testPublicKey(t)})
	if err != nil {
		t.Fatalf("encodeResponse() error = %v", err)
	}

	tooLong := bytes.Clone(validPayload[:pairOKFixedSize])
	binary.BigEndian.PutUint16(tooLong[ed25519.PublicKeySize:], MaxPeerNameSize+1)

	for _, payload := range [][]byte{
		nil,
		validPayload[:pairOKFixedSize-1],
		tooLong,
		append(bytes.Clone(validPayload), 0),
	} {
		if _, err := decodeResponse(payload); !errors.Is(err, ErrMalformedMessage) {
			t.Fatalf("decodeResponse() error = %v, want ErrMalformedMessage", err)
		}
	}
}

func validRequestPayload(t *testing.T) []byte {
	t.Helper()
	publicKey, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("GenerateKey() error = %v", err)
	}
	payload, err := encodeRequest(request{
		name:      "alice",
		publicKey: publicKey,
		signature: ed25519.Sign(privateKey, []byte("challenge")),
		code:      "code",
	})
	if err != nil {
		t.Fatalf("encodeRequest() error = %v", err)
	}
	return payload
}
