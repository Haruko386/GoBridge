package transport

import (
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"errors"
	"fmt"
	"math/big"
	"time"

	"github.com/Haruko386/GoBridge/internal/identity"
)

const (
	TLSProtocol = "gobridge/1"

	certificateLifetime = 365 * 24 * time.Hour
	clockSkewAllowance  = 5 * time.Minute
)

var ErrServerIdentityMismatch = errors.New("TLS server identity does not match expected public key")
var ErrTLSProtocolMismatch = errors.New("unexpected TLS application protocol")

func NewServerTLSConfig(nodeIdentity identity.Identity) (*tls.Config, error) {
	if err := nodeIdentity.Validate(); err != nil {
		return nil, fmt.Errorf("validate server identity %w", err)
	}

	certificate, err := newTLSCertificate(nodeIdentity, time.Now())
	if err != nil {
		return nil, fmt.Errorf("create server TLS certificate %w", err)
	}

	return &tls.Config{
		MinVersion:       tls.VersionTLS13,
		Certificates:     []tls.Certificate{certificate},
		NextProtos:       []string{TLSProtocol},
		VerifyConnection: verifyTLSProtocol,
	}, nil
}

func NewClientTLSConfig(
	expectedServerKey ed25519.PublicKey,
) (*tls.Config, error) {
	if len(expectedServerKey) != ed25519.PublicKeySize {
		return nil, fmt.Errorf("invalid expected server public key length %d", len(expectedServerKey))
	}

	// Copy the key so callers cannot change the pinned identity
	// after configuration has been created.
	pinnedKey := bytes.Clone(expectedServerKey)

	return &tls.Config{
		MinVersion: tls.VersionTLS13,
		NextProtos: []string{TLSProtocol},

		// Standard hostname verification cannot be used because GoBridge
		// nodes use self-signed certificates and are identified by their
		// Ed25519 public keys rather than DNS names.
		//
		// Verification is replaced below by explicit public-key pinning
		// and X.509 validity checks.
		InsecureSkipVerify: true, //nolint:gosec

		VerifyConnection: func(state tls.ConnectionState) error {
			if len(state.PeerCertificates) == 0 {
				return errors.New("TLS server did not provide a certificate")
			}

			certificate := state.PeerCertificates[0]

			serverKey, ok := certificate.PublicKey.(ed25519.PublicKey)
			if !ok {
				return errors.New("TLS server certificate does not use Ed25519")
			}

			if !bytes.Equal(serverKey, pinnedKey) {
				return ErrServerIdentityMismatch
			}

			roots := x509.NewCertPool()
			roots.AddCert(certificate)

			if _, err := certificate.Verify(
				x509.VerifyOptions{
					Roots: roots,
					KeyUsages: []x509.ExtKeyUsage{
						x509.ExtKeyUsageServerAuth,
					},
				},
			); err != nil {
				return fmt.Errorf("verify pinned TLS certificate: %w", err)
			}
			return verifyTLSProtocol(state)
		},
	}, nil
}

func newTLSCertificate(nodeIdentity identity.Identity, now time.Time) (tls.Certificate, error) {
	serialLimit := new(big.Int).Lsh(
		big.NewInt(1),
		128,
	)

	serialNumber, err := rand.Int(
		rand.Reader,
		serialLimit,
	)
	if err != nil {
		return tls.Certificate{}, fmt.Errorf("generate certificate serial number: %w", err)
	}

	nodeID := nodeIdentity.NodeID()

	template := &x509.Certificate{
		SerialNumber: serialNumber,
		Subject: pkix.Name{
			CommonName: "gobridge-" + nodeID[:16],
		},
		NotBefore: now.Add(-clockSkewAllowance),
		NotAfter:  now.Add(certificateLifetime),

		KeyUsage: x509.KeyUsageDigitalSignature,

		ExtKeyUsage: []x509.ExtKeyUsage{
			x509.ExtKeyUsageServerAuth,
		},

		BasicConstraintsValid: true,
	}

	certificateDER, err := x509.CreateCertificate(
		rand.Reader,
		template,
		template,
		nodeIdentity.PublicKey(),
		nodeIdentity.PrivateKey(),
	)
	if err != nil {
		return tls.Certificate{}, fmt.Errorf("create self-signed TLS certificate: %w", err)
	}

	leaf, err := x509.ParseCertificate(certificateDER)
	if err != nil {
		return tls.Certificate{}, fmt.Errorf("parse generated TLS certificate: %w", err)
	}

	return tls.Certificate{
		Certificate: [][]byte{certificateDER},
		PrivateKey:  nodeIdentity.PrivateKey(),
		Leaf:        leaf,
	}, nil
}

func verifyTLSProtocol(state tls.ConnectionState) error {
	if state.NegotiatedProtocol != TLSProtocol {
		return fmt.Errorf("%w: got %q, want %q", ErrTLSProtocolMismatch, state.NegotiatedProtocol, TLSProtocol)
	}
	return nil
}
