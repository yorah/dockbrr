// Package secret manages dockbrr's data-encryption key and AES-256-GCM
// sealing used for credentials and tokens stored at rest.
package secret

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
)

const keyFileName = "secret.key"

// LoadOrCreateKey returns the 32-byte key from <dataDir>/secret.key,
// creating it with mode 0600 if it does not exist.
func LoadOrCreateKey(dataDir string) ([]byte, error) {
	path := filepath.Join(dataDir, keyFileName)
	b, err := os.ReadFile(path)
	switch {
	case err == nil:
		if len(b) != 32 {
			return nil, fmt.Errorf("secret key %s has length %d, want 32", path, len(b))
		}
		return b, nil
	case errors.Is(err, os.ErrNotExist):
		key := make([]byte, 32)
		if _, err := rand.Read(key); err != nil {
			return nil, err
		}
		if err := os.WriteFile(path, key, 0o600); err != nil {
			return nil, err
		}
		return key, nil
	default:
		return nil, err
	}
}

// Sealer encrypts and decrypts small secrets with AES-256-GCM.
type Sealer struct{ gcm cipher.AEAD }

func NewSealer(key []byte) (*Sealer, error) {
	if len(key) != 32 {
		return nil, fmt.Errorf("secret: key must be 32 bytes, got %d", len(key))
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	return &Sealer{gcm: gcm}, nil
}

// Seal returns base64(nonce || ciphertext).
func (s *Sealer) Seal(plaintext []byte) (string, error) {
	nonce := make([]byte, s.gcm.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return "", err
	}
	ct := s.gcm.Seal(nonce, nonce, plaintext, nil)
	return base64.StdEncoding.EncodeToString(ct), nil
}

func (s *Sealer) Open(enc string) ([]byte, error) {
	raw, err := base64.StdEncoding.DecodeString(enc)
	if err != nil {
		return nil, err
	}
	ns := s.gcm.NonceSize()
	if len(raw) < ns+s.gcm.Overhead() {
		return nil, errors.New("ciphertext too short")
	}
	return s.gcm.Open(nil, raw[:ns], raw[ns:], nil)
}
