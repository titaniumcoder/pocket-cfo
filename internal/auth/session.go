// Package auth handles GitHub OAuth login and the encrypted session cookie
// that caches the result. See ARCHITECTURE.md §8.
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

// TTL is how long a GitHub-authenticated session stays valid without
// re-checking collaborator permission on GitHub — see ARCHITECTURE.md §8.
const TTL = 10 * time.Minute

// ReadOnlyTTL is how long an email-authenticated (readonly) session stays
// valid. Much longer than TTL since re-validating means emailing the user a
// fresh login link rather than a one-click GitHub redirect — see
// ARCHITECTURE.md §8.
const ReadOnlyTTL = 7 * 24 * time.Hour

// Session is the payload sealed into the session cookie. Parts is only ever
// populated for a readonly (email-OTP) session — see NewReadOnlySession and
// internal/users, which supplies it at login time from users.json. A push/
// admin session (GitHub collaborator) leaves it nil: HasPart always returns
// true for that tier instead of enumerating every part.
type Session struct {
	Login      string    `json:"login"`
	Permission string    `json:"permission"`
	Parts      []string  `json:"parts,omitempty"`
	ExpiresAt  time.Time `json:"expires_at"`
}

// NewSession builds a push/admin-tier session for login, expiring after
// ttl — callers pass TTL. Use NewReadOnlySession for the email-OTP tier,
// which needs Parts set.
func NewSession(login, permission string, ttl time.Duration) Session {
	return Session{Login: login, Permission: permission, ExpiresAt: time.Now().Add(ttl)}
}

// NewReadOnlySession builds a readonly (email-OTP) session scoped to parts,
// expiring after ttl — callers pass ReadOnlyTTL. parts should come straight
// from internal/users.PartsFor at login time.
func NewReadOnlySession(login string, parts []string, ttl time.Duration) Session {
	return Session{Login: login, Permission: "readonly", Parts: parts, ExpiresAt: time.Now().Add(ttl)}
}

// HasPart reports whether the session may access part. A push/admin
// session (GitHub collaborator) always has access to every part; a
// readonly session only has access to what's in Parts.
func (s Session) HasPart(part string) bool {
	if s.Permission == "push" || s.Permission == "admin" {
		return true
	}
	return slices.Contains(s.Parts, part)
}

func (s Session) Expired() bool {
	return time.Now().After(s.ExpiresAt)
}

// key derives a 32-byte AES-256 key from the (arbitrary-length) secret.
func key(secret string) [32]byte {
	return sha256.Sum256([]byte(secret))
}

// Encode JSON-encodes and AES-256-GCM-seals s, returning a value safe to
// put directly in a cookie (base64, URL-safe).
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

// Decode reverses Encode and additionally rejects an expired session.
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
