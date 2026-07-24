package auth

import (
	"bytes"
	"testing"
)

func TestNewSessionToken(t *testing.T) {
	t.Parallel()

	firstToken, firstDigest, err := NewSessionToken()
	if err != nil {
		t.Fatal(err)
	}
	secondToken, secondDigest, err := NewSessionToken()
	if err != nil {
		t.Fatal(err)
	}

	if firstToken == secondToken {
		t.Fatal("session tokens are not unique")
	}
	if bytes.Equal(firstDigest, secondDigest) {
		t.Fatal("session token digests are not unique")
	}
	if !bytes.Equal(firstDigest, DigestSessionToken(firstToken)) {
		t.Fatal("session token digest cannot be reproduced")
	}
	if bytes.Contains(firstDigest, []byte(firstToken)) {
		t.Fatal("session digest contains the plaintext token")
	}
}

func TestNewCSRFToken(t *testing.T) {
	t.Parallel()

	token, digest, err := NewCSRFToken()
	if err != nil {
		t.Fatal(err)
	}
	if token == "" {
		t.Fatal("CSRF token is empty")
	}
	if !bytes.Equal(digest, DigestCSRFToken(token)) {
		t.Fatal("CSRF token digest cannot be reproduced")
	}
}
