package store_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/togettoyou/zke/pkg/server/store"
)

const (
	aiSessionUserID    = "00000000-0000-4000-8000-0000000000b1"
	aiSessionOtherUser = "00000000-0000-4000-8000-0000000000b2"
	aiSessionID        = "00000000-0000-4000-8000-0000000000b3"
	aiSessionTenantID  = "00000000-0000-4000-8000-0000000000c0"
	aiSessionProjectID = "00000000-0000-4000-8000-0000000000c1"
	aiSessionClusterID = "00000000-0000-4000-8000-0000000000c2"
	aiSessionRetention = 30 * 24 * time.Hour
)

func openAISessionStore(t *testing.T, ctx context.Context) *store.AISessionStore {
	t.Helper()

	databaseURL := requireAuthTestDatabaseURL(t)
	pool := openIsolatedDatabase(t, ctx, databaseURL)
	applyMigrations(t, ctx, pool)
	return store.NewAISessionStore(pool)
}

func createAISession(
	t *testing.T,
	ctx context.Context,
	sessionStore *store.AISessionStore,
	id string,
	initiator string,
	now time.Time,
) store.AISession {
	t.Helper()

	session, err := sessionStore.CreateAISession(ctx, store.CreateAISessionParams{
		ID:              id,
		InitiatorUserID: initiator,
		TenantID:        aiSessionTenantID,
		ProjectID:       aiSessionProjectID,
		ClusterID:       aiSessionClusterID,
		Title:           "支付服务半小时前开始报错",
		ApprovalMode:    "assisted",
		Now:             now,
		RetentionCutoff: now.Add(-aiSessionRetention),
	})
	if err != nil {
		t.Fatal(err)
	}
	return session
}

func startAITurn(
	t *testing.T,
	ctx context.Context,
	sessionStore *store.AISessionStore,
	sessionID string,
	now time.Time,
) store.AISessionEvent {
	t.Helper()

	event, err := sessionStore.StartAITurn(ctx, store.StartAITurnParams{
		SessionID:  sessionID,
		Content:    []byte(`{"text":"这是什么情况"}`),
		OccurredAt: now,
	})
	if err != nil {
		t.Fatal(err)
	}
	return event
}

func TestAISessionPersistsSingleClusterBoundary(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	sessionStore := openAISessionStore(t, ctx)
	now := time.Now().UTC()
	session, err := sessionStore.CreateAISession(ctx, store.CreateAISessionParams{
		ID: aiSessionID, InitiatorUserID: aiSessionUserID,
		TenantID: aiSessionTenantID, ProjectID: aiSessionProjectID, ClusterID: aiSessionClusterID,
		Title: "项目巡检", ApprovalMode: "ask", Now: now,
		RetentionCutoff: now.Add(-aiSessionRetention),
	})
	if err != nil {
		t.Fatal(err)
	}
	if session.TenantID != aiSessionTenantID || session.ProjectID != aiSessionProjectID || session.ClusterID != aiSessionClusterID {
		t.Fatalf("unexpected session target: %+v", session)
	}
}

func TestAISessionListStaysInCurrentClusterWorkspace(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	sessionStore := openAISessionStore(t, ctx)
	now := time.Now().UTC()
	createAISession(t, ctx, sessionStore, aiSessionID, aiSessionUserID, now)
	const (
		otherClusterSessionID = "00000000-0000-4000-8000-0000000000c3"
		otherProjectSessionID = "00000000-0000-4000-8000-0000000000c4"
		otherClusterID        = "00000000-0000-4000-8000-0000000000c5"
		otherTenantSessionID  = "00000000-0000-4000-8000-0000000000c6"
		otherProjectID        = "00000000-0000-4000-8000-0000000000c7"
		otherTenantID         = "00000000-0000-4000-8000-0000000000c8"
	)
	otherScopes := []store.CreateAISessionParams{
		{ID: otherClusterSessionID, InitiatorUserID: aiSessionUserID, TenantID: aiSessionTenantID,
			ProjectID: aiSessionProjectID, ClusterID: otherClusterID, Title: "另一个集群"},
		{ID: otherProjectSessionID, InitiatorUserID: aiSessionUserID, TenantID: aiSessionTenantID,
			ProjectID: otherProjectID, ClusterID: aiSessionClusterID, Title: "另一个项目"},
		{ID: otherTenantSessionID, InitiatorUserID: aiSessionUserID, TenantID: otherTenantID,
			ProjectID: aiSessionProjectID, ClusterID: aiSessionClusterID, Title: "另一个租户"},
	}
	for index, input := range otherScopes {
		input.ApprovalMode = "ask"
		input.Now = now.Add(time.Duration(index+1) * time.Second)
		input.RetentionCutoff = now.Add(-aiSessionRetention)
		if _, err := sessionStore.CreateAISession(ctx, input); err != nil {
			t.Fatal(err)
		}
	}

	sessions, err := sessionStore.ListAISessionsForInitiator(
		ctx, aiSessionUserID, aiSessionTenantID, aiSessionProjectID, aiSessionClusterID,
		now.Add(-aiSessionRetention), 50,
	)
	if err != nil {
		t.Fatal(err)
	}
	if len(sessions) != 1 || sessions[0].ID != aiSessionID {
		t.Fatalf("current cluster workspace sessions = %+v", sessions)
	}
	searched, err := sessionStore.SearchAISessionsForInitiator(ctx, store.SearchAISessionsParams{
		InitiatorUserID: aiSessionUserID,
		TenantID:        aiSessionTenantID,
		ProjectID:       aiSessionProjectID,
		ClusterID:       aiSessionClusterID,
		RetentionCutoff: now.Add(-aiSessionRetention),
		Limit:           50,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(searched) != 1 || searched[0].ID != aiSessionID {
		t.Fatalf("searched current cluster workspace sessions = %+v", searched)
	}
}

func appendAISessionEvent(
	t *testing.T,
	ctx context.Context,
	sessionStore *store.AISessionStore,
	sessionID string,
	kind string,
	now time.Time,
) store.AISessionEvent {
	t.Helper()

	event, err := sessionStore.AppendAISessionEvent(ctx, store.AppendAISessionEventParams{
		SessionID:  sessionID,
		Kind:       kind,
		Content:    []byte(`{"text":"entry"}`),
		OccurredAt: now,
		Duration:   250 * time.Millisecond,
	})
	if err != nil {
		t.Fatal(err)
	}
	return event
}

func TestAISessionStartsIdleWithNoTurns(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	sessionStore := openAISessionStore(t, ctx)
	now := time.Now().UTC()
	session := createAISession(t, ctx, sessionStore, aiSessionID, aiSessionUserID, now)

	if session.Status != "idle" || session.CurrentTurn != 0 {
		t.Fatalf("a new session has no turns yet: %+v", session)
	}
	if session.ApprovalMode != "assisted" {
		t.Fatalf("unexpected approval mode: %q", session.ApprovalMode)
	}
	if session.LastTurnStatus != "" || session.LastTurnFailure != "" {
		t.Fatalf("a new session has no outcome yet: %+v", session)
	}
}

// Turn numbers and entry sequences are allocated by the statements that write,
// so a trail is dense and ordered without a second round trip to work out what
// comes next. The sequence runs across turns; the turn number groups them.
func TestAISessionNumbersTurnsAndEntries(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	sessionStore := openAISessionStore(t, ctx)
	now := time.Now().UTC()
	createAISession(t, ctx, sessionStore, aiSessionID, aiSessionUserID, now)

	opened := startAITurn(t, ctx, sessionStore, aiSessionID, now)
	if opened.Turn != 1 || opened.Sequence != 1 || opened.Kind != "input" {
		t.Fatalf("unexpected opening entry: %+v", opened)
	}
	for index, kind := range []string{"system", "tool_call", "conclusion"} {
		event := appendAISessionEvent(t, ctx, sessionStore, aiSessionID, kind, now)
		if event.Sequence != int32(index+2) || event.Turn != 1 {
			t.Fatalf("entry %d landed at %+v", index, event)
		}
	}
	if _, err := sessionStore.FinishAITurn(ctx, store.FinishAITurnParams{
		SessionID: aiSessionID, Status: "succeeded", Now: now,
	}); err != nil {
		t.Fatal(err)
	}

	second := startAITurn(t, ctx, sessionStore, aiSessionID, now)
	if second.Turn != 2 || second.Sequence != 5 {
		t.Fatalf("the second turn must continue the sequence: %+v", second)
	}

	events, err := sessionStore.ListAISessionEventsForInitiator(
		ctx, aiSessionID, aiSessionUserID, 0, now.Add(-aiSessionRetention), 50,
	)
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 5 {
		t.Fatalf("read %d entries", len(events))
	}
	if events[0].Kind != "input" || events[4].Turn != 2 {
		t.Fatalf("entries out of order: %+v", events)
	}
	if events[1].Duration != 250*time.Millisecond {
		t.Fatalf("duration round trip: %s", events[1].Duration)
	}

	// A reconnect asks for what came after what it already has.
	resumed, err := sessionStore.ListAISessionEventsForInitiator(
		ctx, aiSessionID, aiSessionUserID, 4, now.Add(-aiSessionRetention), 50,
	)
	if err != nil {
		t.Fatal(err)
	}
	if len(resumed) != 1 || resumed[0].Sequence != 5 {
		t.Fatalf("resume returned %+v", resumed)
	}
}

// Switching is allowed at any time. While a turn is running the switch is
// appended to the trail, so the record says which mode each part of the turn
// ran under instead of only which mode is set now.
func TestAISessionApprovalModeSwitchesAnytimeAndIsNoted(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	sessionStore := openAISessionStore(t, ctx)
	now := time.Now().UTC()
	cutoff := now.Add(-aiSessionRetention)
	createAISession(t, ctx, sessionStore, aiSessionID, aiSessionUserID, now)

	// Idle: the mode changes and nothing is written to the trail, because the
	// next turn's own entry will carry it.
	switched, err := sessionStore.SetAISessionApprovalMode(
		ctx, aiSessionID, aiSessionUserID, "ask", []byte(`{"mode":"ask"}`), now,
	)
	if err != nil {
		t.Fatal(err)
	}
	if switched.ApprovalMode != "ask" {
		t.Fatalf("unexpected mode: %+v", switched)
	}
	events, err := sessionStore.ListAISessionEventsForInitiator(
		ctx, aiSessionID, aiSessionUserID, 0, cutoff, 50,
	)
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 0 {
		t.Fatalf("an idle switch needs no entry, got %d", len(events))
	}

	// Mid-turn: the switch lands in the trail alongside the question.
	startAITurn(t, ctx, sessionStore, aiSessionID, now)
	if _, err := sessionStore.SetAISessionApprovalMode(
		ctx, aiSessionID, aiSessionUserID, "full", []byte(`{"mode":"full"}`), now,
	); err != nil {
		t.Fatal(err)
	}
	events, err = sessionStore.ListAISessionEventsForInitiator(
		ctx, aiSessionID, aiSessionUserID, 0, cutoff, 50,
	)
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 2 {
		t.Fatalf("read %d entries", len(events))
	}
	note := events[1]
	if note.Kind != "system" || note.Turn != 1 || note.Sequence != 2 {
		t.Fatalf("unexpected note: %+v", note)
	}
	if string(note.Content) != `{"mode": "full"}` {
		t.Fatalf("unexpected note content: %s", note.Content)
	}

	// And it is the initiator's session to change, like everything else here.
	if _, err := sessionStore.SetAISessionApprovalMode(
		ctx, aiSessionID, aiSessionOtherUser, "ask", []byte(`{"mode":"ask"}`), now,
	); !errors.Is(err, store.ErrAISessionNotFound) {
		t.Fatalf("another user must not change the mode, got %v", err)
	}
}

func TestAISessionArchiveRoundTripsAndRefusesWorkingSessions(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	sessionStore := openAISessionStore(t, ctx)
	now := time.Now().UTC().Truncate(time.Microsecond)
	cutoff := now.Add(-aiSessionRetention)
	createAISession(t, ctx, sessionStore, aiSessionID, aiSessionUserID, now)

	archived, err := sessionStore.SetAISessionArchived(
		ctx, aiSessionID, aiSessionUserID, true, now.Add(time.Second), cutoff,
	)
	if err != nil {
		t.Fatal(err)
	}
	if archived.ArchivedAt == nil || !archived.ArchivedAt.Equal(now.Add(time.Second)) {
		t.Fatalf("archive timestamp did not round trip: %+v", archived)
	}

	restored, err := sessionStore.SetAISessionArchived(
		ctx, aiSessionID, aiSessionUserID, false, now.Add(2*time.Second), cutoff,
	)
	if err != nil {
		t.Fatal(err)
	}
	if restored.ArchivedAt != nil {
		t.Fatalf("restored session is still archived: %+v", restored)
	}

	startAITurn(t, ctx, sessionStore, aiSessionID, now.Add(3*time.Second))
	if _, err := sessionStore.SetAISessionArchived(
		ctx, aiSessionID, aiSessionUserID, true, now.Add(4*time.Second), cutoff,
	); !errors.Is(err, store.ErrAISessionBusy) {
		t.Fatalf("working session archive should be busy, got %v", err)
	}
}

func TestAISessionDeleteRequiresArchiveAndCascades(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	sessionStore := openAISessionStore(t, ctx)
	now := time.Now().UTC()
	cutoff := now.Add(-aiSessionRetention)
	createAISession(t, ctx, sessionStore, aiSessionID, aiSessionUserID, now)
	startAITurn(t, ctx, sessionStore, aiSessionID, now)
	if _, err := sessionStore.FinishAITurn(ctx, store.FinishAITurnParams{
		SessionID: aiSessionID, Status: "succeeded", Now: now,
	}); err != nil {
		t.Fatal(err)
	}
	if err := sessionStore.DeleteAISessionForInitiator(
		ctx, aiSessionID, aiSessionUserID, cutoff,
	); !errors.Is(err, store.ErrAISessionNotArchived) {
		t.Fatalf("active session deletion error = %v, want ErrAISessionNotArchived", err)
	}
	if _, err := sessionStore.SetAISessionArchived(
		ctx, aiSessionID, aiSessionUserID, true, now.Add(time.Second), cutoff,
	); err != nil {
		t.Fatal(err)
	}
	if err := sessionStore.DeleteAISessionForInitiator(
		ctx, aiSessionID, aiSessionUserID, cutoff,
	); err != nil {
		t.Fatal(err)
	}
	if _, err := sessionStore.GetAISessionForInitiator(
		ctx, aiSessionID, aiSessionUserID, cutoff,
	); !errors.Is(err, store.ErrAISessionNotFound) {
		t.Fatalf("deleted session lookup error = %v, want ErrAISessionNotFound", err)
	}
	assertNoAISessionRows(t, ctx, sessionStore, aiSessionID)
}

// One session runs one turn at a time, and entries only exist inside a turn.
// Both are the row's own state refusing, not a check a caller has to remember.
func TestAISessionStateGuardsWrites(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	sessionStore := openAISessionStore(t, ctx)
	now := time.Now().UTC()
	createAISession(t, ctx, sessionStore, aiSessionID, aiSessionUserID, now)

	// Nothing may be written before a turn is opened.
	if _, err := sessionStore.AppendAISessionEvent(ctx, store.AppendAISessionEventParams{
		SessionID: aiSessionID, Kind: "conclusion",
		Content: []byte(`{"text":"早了"}`), OccurredAt: now,
	}); !errors.Is(err, store.ErrAISessionIdle) {
		t.Fatalf("expected ErrAISessionIdle, got %v", err)
	}
	if _, err := sessionStore.FinishAITurn(ctx, store.FinishAITurnParams{
		SessionID: aiSessionID, Status: "succeeded", Now: now,
	}); !errors.Is(err, store.ErrAISessionIdle) {
		t.Fatalf("expected ErrAISessionIdle, got %v", err)
	}

	startAITurn(t, ctx, sessionStore, aiSessionID, now)
	if _, err := sessionStore.StartAITurn(ctx, store.StartAITurnParams{
		SessionID: aiSessionID, Content: []byte(`{"text":"插一句"}`), OccurredAt: now,
	}); !errors.Is(err, store.ErrAISessionBusy) {
		t.Fatalf("expected ErrAISessionBusy, got %v", err)
	}

	finished, err := sessionStore.FinishAITurn(ctx, store.FinishAITurnParams{
		SessionID: aiSessionID, Status: "failed", Failure: "model_timeout", Now: now,
	})
	if err != nil {
		t.Fatal(err)
	}
	if finished.Status != "idle" || finished.LastTurnFailure != "model_timeout" {
		t.Fatalf("unexpected outcome: %+v", finished)
	}
	if _, err := sessionStore.FinishAITurn(ctx, store.FinishAITurnParams{
		SessionID: aiSessionID, Status: "succeeded", Now: now,
	}); !errors.Is(err, store.ErrAISessionIdle) {
		t.Fatalf("a turn ends once, got %v", err)
	}
}

// A state-guarded statement matches no row for two different reasons, and the
// caller has to be able to tell them apart.
func TestAISessionUnknownSessionIsNotAStateError(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	sessionStore := openAISessionStore(t, ctx)
	const missing = "00000000-0000-4000-8000-0000000000bf"
	now := time.Now().UTC()

	if _, err := sessionStore.StartAITurn(ctx, store.StartAITurnParams{
		SessionID: missing, Content: []byte(`{"text":"问题"}`), OccurredAt: now,
	}); !errors.Is(err, store.ErrAISessionNotFound) {
		t.Fatalf("expected ErrAISessionNotFound, got %v", err)
	}
	if _, err := sessionStore.AppendAISessionEvent(ctx, store.AppendAISessionEventParams{
		SessionID: missing, Kind: "conclusion",
		Content: []byte(`{"text":"结论"}`), OccurredAt: now,
	}); !errors.Is(err, store.ErrAISessionNotFound) {
		t.Fatalf("expected ErrAISessionNotFound, got %v", err)
	}
}

// The trail is the initiator's and nobody else's. The rule is in the query, so
// a caller cannot reach entries by skipping a check.
func TestAISessionTrailIsReadableOnlyByItsInitiator(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	sessionStore := openAISessionStore(t, ctx)
	now := time.Now().UTC()
	cutoff := now.Add(-aiSessionRetention)
	createAISession(t, ctx, sessionStore, aiSessionID, aiSessionUserID, now)
	startAITurn(t, ctx, sessionStore, aiSessionID, now)

	if _, err := sessionStore.GetAISessionForInitiator(
		ctx, aiSessionID, aiSessionOtherUser, cutoff,
	); !errors.Is(err, store.ErrAISessionNotFound) {
		t.Fatalf("another user must not reach the session, got %v", err)
	}
	events, err := sessionStore.ListAISessionEventsForInitiator(
		ctx, aiSessionID, aiSessionOtherUser, 0, cutoff, 50,
	)
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 0 {
		t.Fatalf("another user read %d entries", len(events))
	}
	sessions, err := sessionStore.ListAISessionsForInitiator(
		ctx, aiSessionOtherUser, aiSessionTenantID, aiSessionProjectID, aiSessionClusterID, cutoff, 50,
	)
	if err != nil {
		t.Fatal(err)
	}
	if len(sessions) != 0 {
		t.Fatalf("another user listed %d sessions", len(sessions))
	}
}

// Retention runs from the last activity, so a session in use does not vanish on
// a timer set when it was created, and one nobody has touched is reclaimed on
// the next write.
func TestAISessionAgesOutFromLastActivity(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	sessionStore := openAISessionStore(t, ctx)
	old := time.Now().UTC().Add(-2 * time.Hour)
	createAISession(t, ctx, sessionStore, aiSessionID, aiSessionUserID, old)
	startAITurn(t, ctx, sessionStore, aiSessionID, old)

	now := time.Now().UTC()
	cutoff := now.Add(-time.Hour)
	if _, err := sessionStore.GetAISessionForInitiator(
		ctx, aiSessionID, aiSessionUserID, cutoff,
	); !errors.Is(err, store.ErrAISessionNotFound) {
		t.Fatalf("an aged-out session must not be readable, got %v", err)
	}

	// Creating the next session reclaims it, entries included through the
	// foreign key.
	const secondSessionID = "00000000-0000-4000-8000-0000000000b4"
	if _, err := sessionStore.CreateAISession(ctx, store.CreateAISessionParams{
		ID: secondSessionID, InitiatorUserID: aiSessionUserID, Title: "新会话",
		TenantID: aiSessionTenantID, ProjectID: aiSessionProjectID, ClusterID: aiSessionClusterID,
		ApprovalMode: "assisted", Now: now, RetentionCutoff: cutoff,
	}); err != nil {
		t.Fatal(err)
	}
	assertNoAISessionRows(t, ctx, sessionStore, aiSessionID)
}

// A session still marked working after the Server that drove it is gone
// describes something that is not happening, and its trail must say why it
// stops rather than simply stopping.
func TestAISessionInterruptedTurnsAreEndedAtStartup(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	sessionStore := openAISessionStore(t, ctx)
	now := time.Now().UTC()
	cutoff := now.Add(-aiSessionRetention)
	createAISession(t, ctx, sessionStore, aiSessionID, aiSessionUserID, now)
	startAITurn(t, ctx, sessionStore, aiSessionID, now)

	ended, err := sessionStore.InterruptAITurns(ctx, store.InterruptAITurnsParams{
		Failure: "interrupted",
		Content: []byte(`{"failure":"interrupted"}`),
		Now:     now,
	})
	if err != nil {
		t.Fatal(err)
	}
	if ended != 1 {
		t.Fatalf("ended %d turns", ended)
	}

	session, err := sessionStore.GetAISessionForInitiator(ctx, aiSessionID, aiSessionUserID, cutoff)
	if err != nil {
		t.Fatal(err)
	}
	if session.Status != "idle" || session.LastTurnFailure != "interrupted" {
		t.Fatalf("unexpected recovered state: %+v", session)
	}
	events, err := sessionStore.ListAISessionEventsForInitiator(
		ctx, aiSessionID, aiSessionUserID, 0, cutoff, 50,
	)
	if err != nil {
		t.Fatal(err)
	}
	last := events[len(events)-1]
	if last.Kind != "error" || last.Turn != 1 {
		t.Fatalf("the trail must end with the interruption: %+v", last)
	}

	// Nothing is left to recover on the next start.
	again, err := sessionStore.InterruptAITurns(ctx, store.InterruptAITurnsParams{
		Failure: "interrupted", Content: []byte(`{"failure":"interrupted"}`), Now: now,
	})
	if err != nil {
		t.Fatal(err)
	}
	if again != 0 {
		t.Fatalf("recovered %d turns a second time", again)
	}
}

func assertNoAISessionRows(
	t *testing.T,
	ctx context.Context,
	sessionStore *store.AISessionStore,
	sessionID string,
) {
	t.Helper()

	events, err := sessionStore.ListAISessionEventsForInitiator(
		ctx, sessionID, aiSessionUserID, 0, time.Time{}, 50,
	)
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 0 {
		t.Fatalf("%d entries of a reclaimed session survived", len(events))
	}
}
