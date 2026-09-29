package pairing

import (
	"bytes"
	"crypto/ed25519"
	"encoding/binary"
	"errors"
	"fmt"
	"strings"
	"unicode/utf8"
)

const (
	MaxPeerNameSize = 128
	MaxCodeSize     = 256

	pairRequestFixedSize = ed25519.PublicKeySize + ed25519.SignatureSize + 2 + 2
	pairOKFixedSize      = ed25519.PublicKeySize + 2
)

var ErrMalformedMessage = errors.New("malformed pairing message")

type request struct {
	name      string
	publicKey ed25519.PublicKey
	signature []byte
	code      string
}

type response struct {
	name      string
	publicKey ed25519.PublicKey
}

func validatePeerName(name string) error {
	if name == "" {
		return errors.New("peer name must not be empty")
	}

	if !utf8.ValidString(name) {
		return errors.New("peer name is not valid UTF-8")
	}

	if name != strings.TrimSpace(name) {
		return errors.New("peer name must not contain surrounding whitespace")
	}

	if len([]byte(name)) > MaxPeerNameSize {
		return fmt.Errorf("peer name is too long: got %d bytes, maximum is %d", len([]byte(name)), MaxPeerNameSize)
	}

	return nil
}

func encodeRequest(value request) ([]byte, error) {
	if err := validatePeerName(value.name); err != nil {
		return nil, err
	}

	if len(value.publicKey) != ed25519.PublicKeySize {
		return nil, fmt.Errorf("invalid public key: got %d bytes, want %d", len(value.publicKey), ed25519.PublicKeySize)
	}

	if len(value.signature) != ed25519.SignatureSize {
		return nil, fmt.Errorf("invalid signature: got %d bytes, want %d", len(value.signature), ed25519.SignatureSize)
	}

	if value.code == "" {
		return nil, errors.New("code must not be empty")
	}

	if len(value.code) > MaxCodeSize {
		return nil, fmt.Errorf("invalid code: got %d bytes, maximum is %d", len(value.code), MaxCodeSize)
	}

	nameBytes := []byte(value.name)
	codeBytes := []byte(value.code)

	payload := make([]byte, pairRequestFixedSize+len(nameBytes)+len(codeBytes))

	offset := 0

	copy(payload[offset:offset+ed25519.PublicKeySize], value.publicKey)
	offset += ed25519.PublicKeySize

	copy(payload[offset:offset+ed25519.SignatureSize], value.signature)
	offset += ed25519.SignatureSize

	binary.BigEndian.PutUint16(payload[offset:offset+2], uint16(len(nameBytes)))
	offset += 2

	binary.BigEndian.PutUint16(payload[offset:offset+2], uint16(len(codeBytes)))
	offset += 2

	copy(payload[offset:offset+len(nameBytes)], nameBytes)
	offset += len(nameBytes)

	copy(payload[offset:], codeBytes)

	return payload, nil
}

func decodeRequest(payload []byte) (request, error) {
	if len(payload) < pairRequestFixedSize {
		return request{}, fmt.Errorf("%w: request is too short", ErrMalformedMessage)
	}

	offset := 0

	publicKey := bytes.Clone(payload[offset : offset+ed25519.PublicKeySize])
	offset += ed25519.PublicKeySize

	signature := bytes.Clone(payload[offset : offset+ed25519.SignatureSize])
	offset += ed25519.SignatureSize

	nameSize := int(binary.BigEndian.Uint16(payload[offset : offset+2]))
	offset += 2

	codeSize := int(binary.BigEndian.Uint16(payload[offset : offset+2]))
	offset += 2

	expectedSize := pairRequestFixedSize + nameSize + codeSize

	if len(payload) != expectedSize {
		return request{}, fmt.Errorf("%w: got %d bytes, want %d", ErrMalformedMessage, len(payload), expectedSize)
	}

	if nameSize > MaxPeerNameSize {
		return request{}, fmt.Errorf("%w: peer name is too long", ErrMalformedMessage)
	}

	if codeSize > MaxCodeSize {
		return request{}, fmt.Errorf("%w: pair code is too long", ErrMalformedMessage)
	}

	name := string(payload[offset : offset+nameSize])
	offset += nameSize

	code := string(payload[offset : offset+codeSize])

	if err := validatePeerName(name); err != nil {
		return request{}, fmt.Errorf("%w: %v", ErrMalformedMessage, err)
	}

	if code == "" {
		return request{}, fmt.Errorf("%w: code must not be empty", ErrMalformedMessage)
	}

	return request{
		name:      name,
		publicKey: publicKey,
		signature: signature,
		code:      code,
	}, nil
}

func encodeResponse(value response) ([]byte, error) {
	if err := validatePeerName(value.name); err != nil {
		return nil, err
	}
	if len(value.publicKey) != ed25519.PublicKeySize {
		return nil, fmt.Errorf("invalid public key length %d", len(value.publicKey))
	}

	nameBytes := []byte(value.name)
	payload := make([]byte, pairOKFixedSize+len(nameBytes))

	copy(payload[:ed25519.PublicKeySize], value.publicKey)

	binary.BigEndian.PutUint16(
		payload[ed25519.PublicKeySize:pairOKFixedSize],
		uint16(len(nameBytes)),
	)
	copy(payload[pairOKFixedSize:], nameBytes)

	return payload, nil
}

func decodeResponse(payload []byte) (response, error) {
	if len(payload) < pairOKFixedSize {
		return response{}, fmt.Errorf("%w: response is too short", ErrMalformedMessage)
	}

	publicKey := bytes.Clone(payload[:ed25519.PublicKeySize])

	nameSize := int(binary.BigEndian.Uint16(
		payload[ed25519.PublicKeySize:pairOKFixedSize],
	))

	if nameSize > MaxPeerNameSize {
		return response{}, fmt.Errorf("%w: peer name is too long", ErrMalformedMessage)
	}

	expectedSize := pairOKFixedSize + nameSize
	if len(payload) != expectedSize {
		return response{}, fmt.Errorf("%w: got %d bytes, want %d", ErrMalformedMessage, len(payload), expectedSize)
	}

	name := string(payload[pairOKFixedSize:])

	if err := validatePeerName(name); err != nil {
		return response{}, fmt.Errorf("%w: %v", ErrMalformedMessage, err)
	}

	return response{
		name:      name,
		publicKey: ed25519.PublicKey(publicKey),
	}, nil
}
