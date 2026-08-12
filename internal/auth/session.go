package auth

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"slices"
	"time"
)

const TTL = 10 * time.Minute

const ReadOnlyTTL = 7 * 24 * time.Hour

type Session struct {
	Login      string    `json:"login"`
	Permission string    `json:"permission"`
	Parts      []string  `json:"parts,omitempty"`
	ExpiresAt  time.Time `json:"expires_at"`
}

func NewSession(login, permission string, ttl time.Duration) Session {
	return Session{Login: login, Permission: permission, ExpiresAt: time.Now().Add(ttl)}
}

func NewReadOnlySession(login string, parts []string, ttl time.Duration) Session {
	return Session{Login: login, Permission: "readonly", Parts: parts, ExpiresAt: time.Now().Add(ttl)}
}

func (s Session) HasPart(part string) bool {
	if s.Permission == "push" || s.Permission == "admin" {
		return true
	}
	return slices.Contains(s.Parts, part)
}

func (s Session) Expired() bool {
	return time.Now().After(s.ExpiresAt)
}

func key(secret string) [32]byte {
	return sha256.Sum256([]byte(secret))
}

func Encode(secret string, s Session) (string, error) {
	plaintext, err := json.Marshal(s)
	if err != nil {
		return "", fmt.Errorf("marshal session: %w", err)
	}

	k := key(secret)
	block, err := aes.NewCipher(k[:])
	if err != nil {
		return "", fmt.Errorf("new cipher: %w", err)
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return "", fmt.Errorf("new gcm: %w", err)
	}
	nonce := make([]byte, gcm.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return "", fmt.Errorf("generate nonce: %w", err)
	}

	sealed := gcm.Seal(nonce, nonce, plaintext, nil)
	return base64.URLEncoding.EncodeToString(sealed), nil
}

func Decode(secret, value string) (Session, error) {
	var s Session

	sealed, err := base64.URLEncoding.DecodeString(value)
	if err != nil {
		return s, fmt.Errorf("decode base64: %w", err)
	}

	k := key(secret)
	block, err := aes.NewCipher(k[:])
	if err != nil {
		return s, fmt.Errorf("new cipher: %w", err)
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return s, fmt.Errorf("new gcm: %w", err)
	}
	if len(sealed) < gcm.NonceSize() {
		return s, errors.New("session value too short")
	}
	nonce, ciphertext := sealed[:gcm.NonceSize()], sealed[gcm.NonceSize():]

	plaintext, err := gcm.Open(nil, nonce, ciphertext, nil)
	if err != nil {
		return s, fmt.Errorf("decrypt: %w", err)
	}
	if err := json.Unmarshal(plaintext, &s); err != nil {
		return s, fmt.Errorf("unmarshal session: %w", err)
	}
	if s.Expired() {
		return s, errors.New("session expired")
	}
	return s, nil
}
