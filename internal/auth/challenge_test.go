package auth

import (
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"errors"
	"testing"
)

func TestGenerateChallenge(t *testing.T) {
	first, err := GenerateChallenge()
	if err != nil {
		t.Fatalf(
			"first GenerateChallenge() error = %v",
			err,
		)
	}

	second, err := GenerateChallenge()
	if err != nil {
		t.Fatalf(
			"second GenerateChallenge() error = %v",
			err,
		)
	}

	if first == (Challenge{}) {
		t.Fatal("GenerateChallenge() returned zero challenge")
	}

	if first == second {
		t.Fatal("two generated challenges are identical")
	}
}

func TestParseChallenge(t *testing.T) {
	input := make([]byte, ChallengeSize)

	for index := range input {
		input[index] = byte(index)
	}

	challenge, err := ParseChallenge(input)
	if err != nil {
		t.Fatalf("ParseChallenge() error = %v", err)
	}

	if !bytes.Equal(challenge.Bytes(), input) {
		t.Fatalf(
			"ParseChallenge() = %v, want %v",
			challenge.Bytes(),
			input,
		)
	}

	// ParseChallenge must copy the caller's data.
	input[0] ^= 0xff

	if challenge[0] == input[0] {
		t.Fatal("modifying input changed parsed challenge")
	}
}

func TestParseChallengeRejectsInvalidLength(t *testing.T) {
	tests := []struct {
		name string
		data []byte
	}{
		{
			name: "empty",
			data: nil,
		},
		{
			name: "too short",
			data: make([]byte, ChallengeSize-1),
		},
		{
			name: "too long",
			data: make([]byte, ChallengeSize+1),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := ParseChallenge(tt.data); err == nil {
				t.Fatal(
					"ParseChallenge() error = nil, want an error",
				)
			}
		})
	}
}

func TestChallengeBytesReturnsCopy(t *testing.T) {
	challenge, err := GenerateChallenge()
	if err != nil {
		t.Fatalf("GenerateChallenge() error = %v", err)
	}

	original := challenge
	data := challenge.Bytes()
	data[0] ^= 0xff

	if challenge != original {
		t.Fatal("modifying Bytes() result changed challenge")
	}
}

func TestSignAndVerify(t *testing.T) {
	publicKey, privateKey := generateKeyPair(t)

	challenge, err := GenerateChallenge()
	if err != nil {
		t.Fatalf("GenerateChallenge() error = %v", err)
	}

	signature, err := Sign(privateKey, challenge)
	if err != nil {
		t.Fatalf("Sign() error = %v", err)
	}

	if len(signature) != ed25519.SignatureSize {
		t.Errorf(
			"signature length = %d, want %d",
			len(signature),
			ed25519.SignatureSize,
		)
	}

	if err := Verify(
		publicKey,
		challenge,
		signature,
	); err != nil {
		t.Fatalf("Verify() error = %v", err)
	}
}

func TestVerifyRejectsInvalidProof(t *testing.T) {
	publicKey, privateKey := generateKeyPair(t)
	otherPublicKey, _ := generateKeyPair(t)

	challenge, err := GenerateChallenge()
	if err != nil {
		t.Fatalf("GenerateChallenge() error = %v", err)
	}

	signature, err := Sign(privateKey, challenge)
	if err != nil {
		t.Fatalf("Sign() error = %v", err)
	}

	differentChallenge := challenge
	differentChallenge[0] ^= 0xff

	tamperedSignature := bytes.Clone(signature)
	tamperedSignature[0] ^= 0xff

	tests := []struct {
		name      string
		publicKey ed25519.PublicKey
		challenge Challenge
		signature []byte
	}{
		{
			name:      "wrong public key",
			publicKey: otherPublicKey,
			challenge: challenge,
			signature: signature,
		},
		{
			name:      "different challenge",
			publicKey: publicKey,
			challenge: differentChallenge,
			signature: signature,
		},
		{
			name:      "tampered signature",
			publicKey: publicKey,
			challenge: challenge,
			signature: tamperedSignature,
		},
		{
			name:      "empty signature",
			publicKey: publicKey,
			challenge: challenge,
			signature: nil,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := Verify(
				tt.publicKey,
				tt.challenge,
				tt.signature,
			)

			if !errors.Is(err, ErrInvalidSignature) {
				t.Fatalf(
					"Verify() error = %v, want ErrInvalidSignature",
					err,
				)
			}
		})
	}
}

func TestSignRejectsInvalidPrivateKey(t *testing.T) {
	challenge, err := GenerateChallenge()
	if err != nil {
		t.Fatalf("GenerateChallenge() error = %v", err)
	}

	_, err = Sign(
		ed25519.PrivateKey("invalid"),
		challenge,
	)

	if err == nil {
		t.Fatal("Sign() error = nil, want an error")
	}
}

func TestVerifyRejectsInvalidPublicKey(t *testing.T) {
	challenge, err := GenerateChallenge()
	if err != nil {
		t.Fatalf("GenerateChallenge() error = %v", err)
	}

	signature := make([]byte, ed25519.SignatureSize)

	err = Verify(
		ed25519.PublicKey("invalid"),
		challenge,
		signature,
	)

	if err == nil {
		t.Fatal("Verify() error = nil, want an error")
	}
}

func generateKeyPair(
	t *testing.T,
) (ed25519.PublicKey, ed25519.PrivateKey) {
	t.Helper()

	publicKey, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("ed25519.GenerateKey() error = %v", err)
	}

	return publicKey, privateKey
}
