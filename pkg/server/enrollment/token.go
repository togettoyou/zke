package enrollment

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
)

const tokenBytes = 32

func newToken() (string, []byte, error) {
	randomValue := make([]byte, tokenBytes)
	if _, err := rand.Read(randomValue); err != nil {
		return "", nil, errors.New("generate enrollment token")
	}
	token := base64.RawURLEncoding.EncodeToString(randomValue)
	digest := sha256.Sum256([]byte(token))
	return token, digest[:], nil
}

func digestToken(token string) ([]byte, error) {
	decoded, err := base64.RawURLEncoding.DecodeString(token)
	if err != nil || len(decoded) != tokenBytes {
		return nil, ErrTokenRejected
	}
	digest := sha256.Sum256([]byte(token))
	return digest[:], nil
}
