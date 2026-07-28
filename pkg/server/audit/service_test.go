package audit

import (
	"context"
	"testing"

	"github.com/togettoyou/zke/pkg/server/auditaction"
)

func TestRecordProjectEventRejectsInvalidInputBeforeStoreAccess(t *testing.T) {
	t.Parallel()

	service := NewService(nil, nil)
	err := service.RecordProjectEvent(context.Background(), ProjectEventInput{
		ActorUserID: "not-a-uuid",
		ProjectID:   "00000000-0000-0000-0000-000000000001",
		Action:      auditaction.ClusterEnrollmentCreate,
		Result:      "denied",
		RequestID:   "request-1",
	})
	if err == nil {
		t.Fatal("RecordProjectEvent() accepted invalid input")
	}
}
