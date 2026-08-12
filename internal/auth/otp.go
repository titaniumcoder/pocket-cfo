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

type otpPayload struct {
	Email     string    `json:"email"`
	ExpiresAt time.Time `json:"expires_at"`
}

func GenerateLoginToken(secret, email string, ttl time.Duration) (string, error) {
	payload := otpPayload{Email: email, ExpiresAt: time.Now().Add(ttl)}
	raw, err := json.Marshal(payload)
	if err != nil {
		return "", fmt.Errorf("marshal otp payload: %w", err)
	}
	payloadB64 := base64.URLEncoding.EncodeToString(raw)
	return payloadB64 + "." + signOTP(secret, payloadB64), nil
}

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
