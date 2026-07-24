package auth

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestServiceLimitsConcurrentPasswordChecks(t *testing.T) {
	t.Parallel()

	service := testService()
	entered := make(chan struct{}, 2)
	release := make(chan struct{})
	service.passwordVerifier = func([]byte, string) (bool, bool, error) {
		entered <- struct{}{}
		<-release
		return false, false, nil
	}

	firstDone := make(chan error, 1)
	go func() {
		_, _, err := service.verifyPassword(
			context.Background(),
			[]byte("password"),
			"hash",
		)
		firstDone <- err
	}()
	<-entered

	waitContext, cancelWait := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancelWait()
	if _, _, err := service.verifyPassword(
		waitContext,
		[]byte("password"),
		"hash",
	); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("queued password check error = %v, want deadline exceeded", err)
	}
	select {
	case <-entered:
		t.Fatal("second password verifier entered above the concurrency limit")
	default:
	}

	close(release)
	if err := <-firstDone; err != nil {
		t.Fatal(err)
	}
}

func TestServiceAuthenticateRejectsEmptyToken(t *testing.T) {
	t.Parallel()

	_, err := testService().Authenticate(
		context.Background(),
		"",
		time.Now().UTC(),
	)
	if !errors.Is(err, ErrUnauthenticated) {
		t.Fatalf("Authenticate() error = %v, want ErrUnauthenticated", err)
	}
}

func TestCSRFTokenMatches(t *testing.T) {
	t.Parallel()

	token, digest, err := NewCSRFToken()
	if err != nil {
		t.Fatal(err)
	}
	if !CSRFTokenMatches(token, digest) {
		t.Fatal("CSRFTokenMatches() rejected the correct token")
	}
	if CSRFTokenMatches("wrong-token", digest) {
		t.Fatal("CSRFTokenMatches() accepted an incorrect token")
	}
}

func testService() *Service {
	service := NewService(nil, ServiceConfig{
		SessionIdleTimeout:          30 * time.Minute,
		SessionAbsoluteTimeout:      8 * time.Hour,
		MaxConcurrentPasswordChecks: 1,
	})
	service.passwordParams = testPasswordParams
	return service
}
