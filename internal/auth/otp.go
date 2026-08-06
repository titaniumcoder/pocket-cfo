package auth

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
)

// otpPayload is the signed contents of an email login link — self-expiring,
// so unlike a session cookie there is nothing to look up server-side to
// invalidate it early. See ARCHITECTURE.md §8.
type otpPayload struct {
	Email     string    `json:"email"`
	ExpiresAt time.Time `json:"expires_at"`
}

// GenerateLoginToken builds a signed, self-expiring token for email, valid
// for ttl. The token is base64url(payload) + "." + base64url(HMAC-SHA256 of
// the payload), the same shape as the /client/{token} portal links in
// cmd/pocketcfo/client.go but bundling an expiry instead of relying on a
// separately-tracked passkey.
func GenerateLoginToken(secret, email string, ttl time.Duration) (string, error) {
	payload := otpPayload{Email: email, ExpiresAt: time.Now().Add(ttl)}
	raw, err := json.Marshal(payload)
	if err != nil {
		return "", fmt.Errorf("marshal otp payload: %w", err)
	}
	payloadB64 := base64.URLEncoding.EncodeToString(raw)
	return payloadB64 + "." + signOTP(secret, payloadB64), nil
}

// VerifyLoginToken checks token's signature and expiry, returning the email
// it was issued for on success.
func VerifyLoginToken(secret, token string) (string, error) {
	payloadB64, sig, ok := strings.Cut(token, ".")
	if !ok {
		return "", errors.New("malformed token")
	}
	if !hmac.Equal([]byte(signOTP(secret, payloadB64)), []byte(sig)) {
		return "", errors.New("invalid signature")
	}

	raw, err := base64.URLEncoding.DecodeString(payloadB64)
	if err != nil {
		return "", fmt.Errorf("decode payload: %w", err)
	}
	var payload otpPayload
	if err := json.Unmarshal(raw, &payload); err != nil {
		return "", fmt.Errorf("unmarshal payload: %w", err)
	}
	if time.Now().After(payload.ExpiresAt) {
		return "", errors.New("token expired")
	}
	return payload.Email, nil
}

func signOTP(secret, payloadB64 string) string {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(payloadB64))
	return base64.URLEncoding.EncodeToString(mac.Sum(nil))
}
