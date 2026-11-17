package reality

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/ecdh"
	"crypto/ed25519"
	"crypto/hmac"
	"crypto/sha256"
	"crypto/sha512"
	"crypto/x509"
	"encoding/base64"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"io"
	"net"
	"time"

	"github.com/KeepS13/HQIP/internal/model"
	utls "github.com/metacubex/utls"
	"golang.org/x/crypto/hkdf"
)

// Handshake authenticates the Reality server only. The caller owns conn on
// success and failure. No VLESS UUID, destination, proxy lease or traffic is
// sent, so this is safe for public endpoint latency checks and startup checks.
// The wire contract matches the pinned sing-box desktop core's Reality client:
// X25519, HKDF-SHA256("REALITY"), AES-GCM session ID and an HMAC-SHA512 certificate.
func Handshake(ctx context.Context, conn net.Conn, endpoint model.RealityEndpoint) (net.Conn, error) {
	if err := endpoint.Validate(); err != nil {
		return nil, err
	}
	publicBytes, _ := base64.RawURLEncoding.DecodeString(endpoint.PublicKey)
	public, err := ecdh.X25519().NewPublicKey(publicBytes)
	if err != nil {
		return nil, err
	}
	short, _ := hex.DecodeString(endpoint.ShortID)
	var authKey []byte
	verified := false
	config := &utls.Config{ServerName: endpoint.ServerName, SessionTicketsDisabled: true,
		// Standard PKI is replaced with the mandatory pinned Reality proof below.
		// A genuine fallback-site certificate is deliberately NOT a successful probe.
		InsecureSkipVerify: true, MinVersion: utls.VersionTLS13,
		VerifyPeerCertificate: func(raw [][]byte, _ [][]*x509.Certificate) error {
			if len(raw) == 0 || len(authKey) != 32 {
				return errors.New("missing Reality proof")
			}
			certificate, e := x509.ParseCertificate(raw[0])
			if e != nil {
				return e
			}
			key, ok := certificate.PublicKey.(ed25519.PublicKey)
			if !ok {
				return errors.New("invalid Reality proof key")
			}
			proof := hmac.New(sha512.New, authKey)
			_, _ = proof.Write(key)
			if !hmac.Equal(certificate.Signature, proof.Sum(nil)) {
				return errors.New("Reality verification failed")
			}
			verified = true
			return nil
		},
	}
	client := utls.UClient(conn, config, utls.HelloChrome_Auto)
	if err = client.BuildHandshakeState(); err != nil {
		return nil, err
	}
	// Reality's session proof uses the classical X25519 share. The public
	// endpoint does not negotiate ML-KEM; retain other fingerprint extensions.
	for _, extension := range client.Extensions {
		switch e := extension.(type) {
		case *utls.SupportedCurvesExtension:
			curves := e.Curves[:0]
			for _, curve := range e.Curves {
				if curve != utls.X25519MLKEM768 {
					curves = append(curves, curve)
				}
			}
			e.Curves = curves
		case *utls.KeyShareExtension:
			shares := e.KeyShares[:0]
			for _, share := range e.KeyShares {
				if share.Group != utls.X25519MLKEM768 {
					shares = append(shares, share)
				}
			}
			e.KeyShares = shares
		}
	}
	if err = client.BuildHandshakeState(); err != nil {
		return nil, err
	}
	hello := client.HandshakeState.Hello
	shares := client.HandshakeState.State13.KeyShareKeys
	if shares == nil || shares.Ecdhe == nil || len(hello.Random) != 32 || len(hello.Raw) < 71 || hello.Raw[38] != 32 {
		return nil, errors.New("unsupported Reality ClientHello")
	}
	secret, err := shares.Ecdhe.ECDH(public)
	if err != nil {
		return nil, err
	}
	authKey = make([]byte, 32)
	if _, err = io.ReadFull(hkdf.New(sha256.New, secret, hello.Random[:20], []byte("REALITY")), authKey); err != nil {
		return nil, err
	}
	block, err := aes.NewCipher(authKey)
	if err != nil {
		return nil, err
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	hello.SessionId = make([]byte, 32)
	clear(hello.Raw[39:71]) // TLS session ID is zero in the authenticated transcript.
	proof := make([]byte, 16)
	copy(proof, []byte{1, 8, 1})
	binary.BigEndian.PutUint32(proof[4:8], uint32(time.Now().Unix()))
	copy(proof[8:16], short)
	hello.SessionId = aead.Seal(hello.SessionId[:0], hello.Random[20:], proof, hello.Raw)
	copy(hello.Raw[39:71], hello.SessionId)
	if err = client.HandshakeContext(ctx); err != nil {
		return nil, err
	}
	if !verified {
		return nil, errors.New("Reality proof not verified")
	}
	return client, nil
}
