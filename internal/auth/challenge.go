// Package auth implements GoBridge node authentication primitives.
package auth

import (
	"crypto/ed25519"
	"crypto/rand"
	"errors"
	"fmt"
)

const (
	ChallengeSize = 32

	// domainSeparator ensures a signature produced for GoBridge
	// authentication cannot accidentally be reused by another protocol.
	domainSeparator = "gobridge/auth/challenge/v1\x00"
)

var ErrInvalidSignature = errors.New("invalid authentication signature")

type Challenge [ChallengeSize]byte

// GenerateChallenge generate a random challenge
func GenerateChallenge() (Challenge, error) {
	var challenge Challenge

	if _, err := rand.Read(challenge[:]); err != nil {
		return Challenge{}, fmt.Errorf("generate authentication challenge: %w", err)
	}

	return challenge, nil
}

func ParseChallenge(data []byte) (Challenge, error) {
	if len(data) != ChallengeSize {
		return Challenge{}, fmt.Errorf("invalid challenge size: %d", len(data))
	}

	var challenge Challenge
	copy(challenge[:], data)

	return challenge, nil
}

func (c Challenge) Bytes() []byte {
	result := make([]byte, len(c))
	copy(result, c[:])
	return result
}

func Sign(privateKey ed25519.PrivateKey, challenge Challenge) ([]byte, error) {
	if len(privateKey) != ed25519.PrivateKeySize {
		return nil, fmt.Errorf("invalid Ed25519 private key length %d", len(privateKey))
	}

	signature := ed25519.Sign(privateKey, signingMessage(challenge))
	return signature, nil
}

func Verify(publicKey ed25519.PublicKey, challenge Challenge, signature []byte) error {
	if len(publicKey) != ed25519.PublicKeySize {
		return fmt.Errorf("invalid Ed25519 public key length %d", len(publicKey))
	}
	if len(signature) != ed25519.SignatureSize {
		return fmt.Errorf("%w: invalid signature length %d", ErrInvalidSignature, len(signature))
	}

	if !ed25519.Verify(publicKey, signingMessage(challenge), signature) {
		return ErrInvalidSignature
	}

	return nil
}

func signingMessage(challenge Challenge) []byte {
	message := make([]byte, 0, len(domainSeparator)+len(challenge))

	message = append(message, domainSeparator...)
	message = append(message, challenge[:]...)

	return message
}
