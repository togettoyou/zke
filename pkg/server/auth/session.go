package auth

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
)

const sessionTokenBytes = 32

func NewSessionToken() (string, []byte, error) {
	randomValue := make([]byte, sessionTokenBytes)
	if _, err := rand.Read(randomValue); err != nil {
		return "", nil, errors.New("generate session token")
	}

	token := base64.RawURLEncoding.EncodeToString(randomValue)
	digest := DigestSessionToken(token)
	return token, digest, nil
}

func DigestSessionToken(token string) []byte {
	digest := sha256.Sum256([]byte(token))
	return digest[:]
}
