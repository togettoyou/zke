package agentmanagement

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestRevokeRejectsInvalidInput(t *testing.T) {
	t.Parallel()

	service := NewService(nil)
	_, err := service.Revoke(context.Background(), RevokeInput{
		AgentID:     "not-a-uuid",
		ActorUserID: "00000000-0000-4000-8000-000000000001",
		RequestID:   "request-agent-revoke",
		Now:         time.Now().UTC(),
	})
	if !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("Revoke() error = %v, want ErrInvalidInput", err)
	}
}
