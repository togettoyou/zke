package audit

import (
	"context"
	"testing"

	"github.com/togettoyou/zke/pkg/server/auditaction"
	"github.com/togettoyou/zke/pkg/server/store"
)

type recordingAuditStore struct {
	Store
	projectEvent store.ProjectAuditEvent
	globalEvent  store.GlobalAuditEvent
}

func (recording *recordingAuditStore) RecordProjectEvent(
	_ context.Context,
	event store.ProjectAuditEvent,
) error {
	recording.projectEvent = event
	return nil
}

func (recording *recordingAuditStore) RecordGlobalEvent(
	_ context.Context,
	event store.GlobalAuditEvent,
) error {
	recording.globalEvent = event
	return nil
}

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

func TestRecordProjectEventPreservesEnrollmentTarget(t *testing.T) {
	t.Parallel()

	recording := &recordingAuditStore{}
	service := NewService(recording, nil)
	err := service.RecordProjectEvent(context.Background(), ProjectEventInput{
		ActorUserID: "00000000-0000-4000-8000-000000000001",
		ProjectID:   "00000000-0000-4000-8000-000000000002",
		ProjectName: "Project Alpha",
		Action:      auditaction.ClusterEnrollmentCreate,
		TargetType:  auditaction.TargetEnrollment,
		TargetName:  "cluster-alpha",
		Result:      "failed",
		RequestID:   "request-1",
	})
	if err != nil {
		t.Fatal(err)
	}
	if recording.projectEvent.ProjectName != "Project Alpha" ||
		recording.projectEvent.TargetType != auditaction.TargetEnrollment ||
		recording.projectEvent.TargetID != "" ||
		recording.projectEvent.TargetName != "cluster-alpha" {
		t.Fatalf("unexpected Project audit event: %+v", recording.projectEvent)
	}
}

func TestRecordGlobalEventOmitsMalformedTargetID(t *testing.T) {
	t.Parallel()

	recording := &recordingAuditStore{}
	service := NewService(recording, nil)
	err := service.RecordGlobalEvent(context.Background(), GlobalEventInput{
		ActorUserID: "00000000-0000-4000-8000-000000000001",
		Action:      auditaction.UserUpdate,
		TargetType:  auditaction.TargetUser,
		TargetID:    "not-a-uuid",
		Result:      "failed",
		RequestID:   "request-invalid-target",
	})
	if err != nil {
		t.Fatal(err)
	}
	if recording.globalEvent.TargetID != "" {
		t.Fatalf(
			"malformed audit target ID reached the store: %q",
			recording.globalEvent.TargetID,
		)
	}
}
