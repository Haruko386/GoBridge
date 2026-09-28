package peer

import (
	"bytes"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
)

type Peer struct {
	NodeID    string
	Name      string
	PublicKey ed25519.PublicKey
	Enabled   bool
}

func New(name string, publicKey ed25519.PublicKey) (Peer, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return Peer{}, errors.New("peer name should not be empty")
	}

	if len(publicKey) != ed25519.PublicKeySize {
		return Peer{}, errors.New("peer public key should be of length ed25519.PublicKeySize")
	}

	sum := sha256.Sum256(publicKey)

	return Peer{
		NodeID:    hex.EncodeToString(sum[:]),
		Name:      name,
		PublicKey: bytes.Clone(publicKey),
		Enabled:   true,
	}, nil
}

func (p Peer) Validate() error {
	if strings.TrimSpace(p.Name) == "" {
		return errors.New("peer name should not be empty")
	}

	if len(p.PublicKey) != ed25519.PublicKeySize {
		return errors.New("peer public key should be of length ed25519.PublicKeySize")
	}

	nodeIDBytes, err := hex.DecodeString(p.NodeID)
	if err != nil {
		return fmt.Errorf("decode peer node id: %w", err)
	}

	if len(nodeIDBytes) != sha256.Size {
		return errors.New("peer node id should be of length sha256.Size")
	}

	expectedNodeID := sha256.Sum256(p.PublicKey)
	if !bytes.Equal(nodeIDBytes, expectedNodeID[:]) {
		return errors.New("peer node id does not match public key")
	}
	return nil
}

func clonePeer(source Peer) Peer {
	source.PublicKey = bytes.Clone(source.PublicKey)
	return source
}
