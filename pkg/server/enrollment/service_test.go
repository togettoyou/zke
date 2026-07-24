package enrollment

import (
	"context"
	"crypto/sha256"
	"errors"
	"testing"
	"time"
)

func TestNewTokenUsesSHA256Digest(t *testing.T) {
	t.Parallel()

	token, digest, err := newToken()
	if err != nil {
		t.Fatal(err)
	}
	if token == "" {
		t.Fatal("token is empty")
	}
	expected := sha256.Sum256([]byte(token))
	if string(digest) != string(expected[:]) {
		t.Fatal("token digest does not match token")
	}
}

func TestCreateRejectsInvalidInputBeforeStoreAccess(t *testing.T) {
	t.Parallel()

	service := NewService(nil, DefaultTokenTTL)
	_, err := service.Create(context.Background(), CreateInput{
		ProjectID:      "not-a-uuid",
		UserID:         "00000000-0000-0000-0000-000000000001",
		RequestID:      "request-1",
		IdempotencyKey: "01234567-89ab-cdef-0123-456789abcdef",
		Now:            time.Now(),
	})
	if !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("Create() error = %v, want ErrInvalidInput", err)
	}
}

func TestCreateRejectsInvalidTokenTTLBeforeStoreAccess(t *testing.T) {
	t.Parallel()

	service := NewService(nil, 0)
	_, err := service.Create(context.Background(), CreateInput{
		ProjectID:      "00000000-0000-0000-0000-000000000001",
		UserID:         "00000000-0000-0000-0000-000000000002",
		RequestID:      "request-1",
		IdempotencyKey: "01234567-89ab-cdef-0123-456789abcdef",
		Now:            time.Now(),
	})
	if err == nil {
		t.Fatal("Create() accepted an invalid token TTL")
	}
}

func TestCreateRejectsInvalidIdempotencyKeyBeforeStoreAccess(t *testing.T) {
	t.Parallel()

	service := NewService(nil, DefaultTokenTTL)
	_, err := service.Create(context.Background(), CreateInput{
		ProjectID:      "00000000-0000-0000-0000-000000000001",
		UserID:         "00000000-0000-0000-0000-000000000002",
		RequestID:      "request-1",
		IdempotencyKey: "too-short",
		Now:            time.Now(),
	})
	if !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("Create() error = %v, want ErrInvalidInput", err)
	}
}
