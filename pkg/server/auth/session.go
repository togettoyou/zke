package auth

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
)

const (
	sessionTokenBytes = 32
	csrfTokenBytes    = 32
)

func NewSessionToken() (string, []byte, error) {
	return newOpaqueToken(sessionTokenBytes, "session")
}

func NewCSRFToken() (string, []byte, error) {
	return newOpaqueToken(csrfTokenBytes, "CSRF")
}

func newOpaqueToken(size int, tokenType string) (string, []byte, error) {
	randomValue := make([]byte, size)
	if _, err := rand.Read(randomValue); err != nil {
		return "", nil, errors.New("generate " + tokenType + " token")
	}

	token := base64.RawURLEncoding.EncodeToString(randomValue)
	digest := digestToken(token)
	return token, digest, nil
}

func DigestSessionToken(token string) []byte {
	return digestToken(token)
}

func DigestCSRFToken(token string) []byte {
	return digestToken(token)
}

func digestToken(token string) []byte {
	digest := sha256.Sum256([]byte(token))
	return digest[:]
}
