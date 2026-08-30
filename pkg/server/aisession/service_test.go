package aisession

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/togettoyou/zke/pkg/server/store"
)

const (
	testUserID    = "11111111-1111-4111-8111-111111111111"
	testOtherUser = "22222222-2222-4222-8222-222222222222"
	testTenantID  = "66666666-6666-4666-8666-666666666666"
	testProjectID = "33333333-3333-4333-8333-333333333333"
	testSessionID = "44444444-4444-4444-8444-444444444444"
	testClusterID = "55555555-5555-4555-8555-555555555555"
)

// fakeStore keeps the one invariant the real table enforces that the tests here
// care about: entries belong to a running turn, and a session runs one turn at
// a time.
type fakeStore struct {
	created       store.CreateAISessionParams
	appended      []store.AppendAISessionEventParams
	started       []store.StartAITurnParams
	finished      store.FinishAITurnParams
	working       bool
	turn          int32
	sequence      int32
	interrupted   store.InterruptAITurnsParams
	events        []store.AISessionEvent
	notFound      bool
	mode          string
	modeNote      []byte
	searched      store.SearchAISessionsParams
	title         string
	archived      bool
	deleted       bool
	attachments   []store.AISessionAttachment
	usage         store.AIUsage
	feedback      store.AITurnFeedback
	evaluation    store.AIEvaluation
	quotaExceeded bool
}

func newFakeStore() *fakeStore {
	return &fakeStore{}
}

func (fake *fakeStore) next() int32 {
	fake.sequence++
	return fake.sequence
}

func (fake *fakeStore) CreateAISession(
	_ context.Context,
	input store.CreateAISessionParams,
) (store.AISession, error) {
	fake.created = input
	return store.AISession{
		ID: input.ID, InitiatorUserID: input.InitiatorUserID, Title: input.Title,
		TenantID: input.TenantID, ProjectID: input.ProjectID, ClusterID: input.ClusterID,
		Status: string(StatusIdle), ApprovalMode: input.ApprovalMode,
		CreatedAt: input.Now, LastActivityAt: input.Now,
	}, nil
}

func (fake *fakeStore) SetAISessionApprovalMode(
	_ context.Context,
	sessionID, initiatorUserID, mode string,
	noteContent []byte,
	now time.Time,
) (store.AISession, error) {
	fake.mode = mode
	fake.modeNote = noteContent
	status := StatusIdle
	if fake.working {
		status = StatusWorking
	}
	return store.AISession{
		ID: sessionID, InitiatorUserID: initiatorUserID, Status: string(status),
		ApprovalMode: mode, LastActivityAt: now,
	}, nil
}

func (fake *fakeStore) StartAITurn(
	_ context.Context,
	input store.StartAITurnParams,
) (store.AISessionEvent, error) {
	if fake.quotaExceeded {
		return store.AISessionEvent{}, store.ErrAIQuotaExceeded
	}
	if fake.working {
		return store.AISessionEvent{}, store.ErrAISessionBusy
	}
	fake.working = true
	fake.turn++
	fake.started = append(fake.started, input)
	return store.AISessionEvent{
		Sequence: fake.next(), Turn: fake.turn, Kind: string(KindInput),
		Content: input.Content, Truncated: input.Truncated, OccurredAt: input.OccurredAt,
	}, nil
}

func (fake *fakeStore) GetAIUsage(
	_ context.Context, _, _, _ string, from, to time.Time,
) (store.AIUsage, error) {
	fake.usage.PeriodStart = from
	fake.usage.PeriodEnd = to
	return fake.usage, nil
}

func (fake *fakeStore) UpsertAITurnFeedback(
	_ context.Context, input store.UpsertAITurnFeedbackParams,
) (store.AITurnFeedback, error) {
	createdAt := fake.feedback.CreatedAt
	if createdAt.IsZero() {
		createdAt = input.Now
	}
	fake.feedback = store.AITurnFeedback{SessionID: input.SessionID, Turn: input.Turn,
		Rating: input.Rating, Outcome: input.Outcome, Reasons: input.Reasons,
		Comment: input.Comment, CreatedAt: createdAt, UpdatedAt: input.Now}
	return fake.feedback, nil
}

func (fake *fakeStore) GetAITurnFeedback(
	_ context.Context, _ string, _ int32, _ string,
) (store.AITurnFeedback, error) {
	if fake.feedback.SessionID == "" {
		return store.AITurnFeedback{}, store.ErrAISessionNotFound
	}
	return fake.feedback, nil
}

func (fake *fakeStore) EvaluateAI(
	_ context.Context, _ store.AIEvaluationQuery,
) (store.AIEvaluation, error) {
	return fake.evaluation, nil
}

func (fake *fakeStore) AppendAISessionEvent(
	_ context.Context,
	input store.AppendAISessionEventParams,
) (store.AISessionEvent, error) {
	if !fake.working {
		return store.AISessionEvent{}, store.ErrAISessionIdle
	}
	fake.appended = append(fake.appended, input)
	return store.AISessionEvent{
		Sequence: fake.next(), Turn: fake.turn, Kind: input.Kind, Content: input.Content,
		Truncated: input.Truncated, OccurredAt: input.OccurredAt, Duration: input.Duration,
	}, nil
}

func (fake *fakeStore) FinishAITurn(
	_ context.Context,
	input store.FinishAITurnParams,
) (store.AISession, error) {
	if !fake.working {
		return store.AISession{}, store.ErrAISessionIdle
	}
	fake.working = false
	fake.finished = input
	return store.AISession{
		ID: input.SessionID, Status: string(StatusIdle), CurrentTurn: fake.turn,
		LastTurnStatus: input.Status, LastTurnFailure: input.Failure, LastActivityAt: input.Now,
	}, nil
}

func (fake *fakeStore) GetAISessionForInitiator(
	_ context.Context,
	sessionID, initiatorUserID string,
	_ time.Time,
) (store.AISession, error) {
	if fake.notFound {
		return store.AISession{}, store.ErrAISessionNotFound
	}
	return store.AISession{
		ID: sessionID, InitiatorUserID: initiatorUserID,
		TenantID: testTenantID, ProjectID: testProjectID, ClusterID: testClusterID, Status: string(StatusIdle),
	}, nil
}

func (fake *fakeStore) ListAISessionsForInitiator(
	_ context.Context,
	initiatorUserID string,
	tenantID string,
	projectID string,
	clusterID string,
	_ time.Time,
	_ int,
) ([]store.AISession, error) {
	return []store.AISession{{
		ID: testSessionID, InitiatorUserID: initiatorUserID,
		TenantID: tenantID, ProjectID: projectID, ClusterID: clusterID,
	}}, nil
}

func (fake *fakeStore) ListAISessionEventsForInitiator(
	_ context.Context,
	_, _ string,
	afterSequence int32,
	_ time.Time,
	_ int,
) ([]store.AISessionEvent, error) {
	kept := make([]store.AISessionEvent, 0, len(fake.events))
	for _, event := range fake.events {
		if event.Sequence > afterSequence {
			kept = append(kept, event)
		}
	}
	return kept, nil
}

func (fake *fakeStore) SearchAISessionsForInitiator(
	_ context.Context,
	input store.SearchAISessionsParams,
) ([]store.AISession, error) {
	fake.searched = input
	return []store.AISession{{
		ID: testSessionID, InitiatorUserID: input.InitiatorUserID,
		TenantID: input.TenantID, ProjectID: input.ProjectID, ClusterID: input.ClusterID,
		Title: "控制面异常", ArchivedAt: archivedAt(input.Archived),
	}}, nil
}

func (fake *fakeStore) UpdateAISessionTitle(
	_ context.Context,
	sessionID, initiatorUserID, title string,
	now, _ time.Time,
) (store.AISession, error) {
	fake.title = title
	return store.AISession{
		ID: sessionID, InitiatorUserID: initiatorUserID, Title: title,
		Status: string(StatusIdle), LastActivityAt: now,
	}, nil
}

func (fake *fakeStore) SetAISessionArchived(
	_ context.Context,
	sessionID, initiatorUserID string,
	archived bool,
	now, _ time.Time,
) (store.AISession, error) {
	fake.archived = archived
	return store.AISession{
		ID: sessionID, InitiatorUserID: initiatorUserID,
		Status: string(StatusIdle), ArchivedAt: archivedAt(archived), LastActivityAt: now,
	}, nil
}

func (fake *fakeStore) DeleteAISessionForInitiator(
	_ context.Context, _, _ string, _ time.Time,
) error {
	if !fake.archived {
		return store.ErrAISessionNotArchived
	}
	fake.deleted = true
	return nil
}

func (fake *fakeStore) CreateAISessionAttachment(
	_ context.Context,
	input store.CreateAISessionAttachmentParams,
) (store.AISessionAttachment, error) {
	created := store.AISessionAttachment{
		ID: input.ID, SessionID: input.SessionID, Name: input.Name,
		MediaType: input.MediaType, Content: input.Content, CreatedAt: input.CreatedAt,
	}
	fake.attachments = append(fake.attachments, created)
	return created, nil
}

func (fake *fakeStore) ListAISessionAttachmentsForInitiator(
	_ context.Context,
	_, _ string,
	_ time.Time,
) ([]store.AISessionAttachment, error) {
	return fake.attachments, nil
}

func (fake *fakeStore) DeleteAISessionAttachmentForInitiator(
	_ context.Context,
	_, attachmentID, _ string,
) error {
	for index, attachment := range fake.attachments {
		if attachment.ID == attachmentID {
			fake.attachments = append(fake.attachments[:index], fake.attachments[index+1:]...)
			return nil
		}
	}
	return store.ErrAISessionNotFound
}

func archivedAt(archived bool) *time.Time {
	if !archived {
		return nil
	}
	now := time.Now().UTC()
	return &now
}

func (fake *fakeStore) InterruptAITurns(
	_ context.Context,
	input store.InterruptAITurnsParams,
) (int64, error) {
	fake.interrupted = input
	if !fake.working {
		return 0, nil
	}
	fake.working = false
	return 1, nil
}

func newTestService(fake *fakeStore) *Service {
	return NewService(fake, Config{})
}

func startTurn(t *testing.T, service *Service, question string) Entry {
	t.Helper()

	entry, err := service.StartTurn(context.Background(), StartTurnInput{
		SessionID: testSessionID, InitiatorUserID: testUserID,
		TenantID: testTenantID, ProjectID: testProjectID,
		Content: Content{Text: question}, Now: time.Now().UTC(),
	})
	if err != nil {
		t.Fatal(err)
	}
	return entry
}

func decodeAppended(t *testing.T, fake *fakeStore, index int) Content {
	t.Helper()

	var content Content
	if err := json.Unmarshal(fake.appended[index].Content, &content); err != nil {
		t.Fatal(err)
	}
	return content
}

func TestCreateDerivesATitleAndRetentionCutoff(t *testing.T) {
	t.Parallel()

	fake := newFakeStore()
	now := time.Now().UTC()
	session, err := newTestService(fake).Create(context.Background(), CreateInput{
		InitiatorUserID: testUserID,
		TenantID:        testTenantID,
		ProjectID:       testProjectID,
		ClusterID:       testClusterID,
		Title:           "  支付服务\n半小时前开始报错  ",
		Now:             now,
	})
	if err != nil {
		t.Fatal(err)
	}
	if session.Status != StatusIdle || session.CurrentTurn != 0 {
		t.Fatalf("a new session has no turns yet: %+v", session)
	}
	if session.TenantID != testTenantID || session.ProjectID != testProjectID || session.ClusterID != testClusterID ||
		fake.created.TenantID != testTenantID || fake.created.ProjectID != testProjectID || fake.created.ClusterID != testClusterID {
		t.Fatalf("unexpected session target: session=%+v stored=%+v", session, fake.created)
	}
	// A title is a label in a list: one line, collapsed whitespace.
	if fake.created.Title != "支付服务 半小时前开始报错" {
		t.Fatalf("unexpected title: %q", fake.created.Title)
	}
	// Retention runs from the last activity, so the cutoff travels with the
	// request rather than being frozen into the row.
	if want := now.Add(-defaultRetention); !fake.created.RetentionCutoff.Equal(want) {
		t.Fatalf("cutoff %s, want %s", fake.created.RetentionCutoff, want)
	}
}

func TestCreateRefusesInput(t *testing.T) {
	t.Parallel()

	cases := map[string]CreateInput{
		"发起者不是 UUID": {InitiatorUserID: "operator", TenantID: testTenantID, ProjectID: testProjectID, ClusterID: testClusterID, Title: "x", Now: time.Now().UTC()},
		"没有租户":       {InitiatorUserID: testUserID, ProjectID: testProjectID, ClusterID: testClusterID, Title: "x", Now: time.Now().UTC()},
		"没有项目":       {InitiatorUserID: testUserID, TenantID: testTenantID, ClusterID: testClusterID, Title: "x", Now: time.Now().UTC()},
		"没有集群":       {InitiatorUserID: testUserID, TenantID: testTenantID, ProjectID: testProjectID, Title: "x", Now: time.Now().UTC()},
		"没有标题":       {InitiatorUserID: testUserID, TenantID: testTenantID, ProjectID: testProjectID, ClusterID: testClusterID, Title: "   ", Now: time.Now().UTC()},
		"没有时间":       {InitiatorUserID: testUserID, TenantID: testTenantID, ProjectID: testProjectID, ClusterID: testClusterID, Title: "x"},
	}
	for name, input := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			if _, err := newTestService(newFakeStore()).Create(context.Background(), input); !errors.Is(
				err, ErrInvalidInput,
			) {
				t.Fatalf("expected ErrInvalidInput, got %v", err)
			}
		})
	}
}

// A new cloud operations session asks before it changes anything. An
// operator may opt into broader automation from the composer afterwards.
func TestCreateDefaultsToAskApproval(t *testing.T) {
	t.Parallel()

	fake := newFakeStore()
	session, err := newTestService(fake).Create(context.Background(), CreateInput{
		InitiatorUserID: testUserID, TenantID: testTenantID, ProjectID: testProjectID, ClusterID: testClusterID,
		Title: "问题", Now: time.Now().UTC(),
	})
	if err != nil {
		t.Fatal(err)
	}
	if session.ApprovalMode != ApprovalAsk || fake.created.ApprovalMode != "ask" {
		t.Fatalf("unexpected mode: %+v", session)
	}
}

func TestCreateRefusesAnUnknownApprovalMode(t *testing.T) {
	t.Parallel()

	fake := newFakeStore()
	if _, err := newTestService(fake).Create(context.Background(), CreateInput{
		InitiatorUserID: testUserID, TenantID: testTenantID, ProjectID: testProjectID, ClusterID: testClusterID, Title: "问题",
		ApprovalMode: ApprovalMode("yolo"), Now: time.Now().UTC(),
	}); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("expected ErrInvalidInput, got %v", err)
	}
	if fake.created.ID != "" {
		t.Fatal("a refused session must not reach the store")
	}
}

// The control lives in the composer, so every mode is reachable at any time.
func TestSetApprovalModeAcceptsEveryMode(t *testing.T) {
	t.Parallel()

	for _, mode := range []ApprovalMode{ApprovalAsk, ApprovalAssisted, ApprovalFull} {
		t.Run(string(mode), func(t *testing.T) {
			t.Parallel()

			fake := newFakeStore()
			session, err := newTestService(fake).SetApprovalMode(
				context.Background(), testSessionID, testUserID, mode, time.Now().UTC(),
			)
			if err != nil {
				t.Fatal(err)
			}
			if session.ApprovalMode != mode || fake.mode != string(mode) {
				t.Fatalf("unexpected mode: %+v", session)
			}
		})
	}
}

// Switching while a turn is running is allowed — that is the moment an operator
// most wants it — and the note carries the mode so the trail stays honest about
// which mode each part of the turn ran under.
func TestSetApprovalModeMidTurnIsNoted(t *testing.T) {
	t.Parallel()

	fake := newFakeStore()
	service := newTestService(fake)
	startTurn(t, service, "问题")

	if _, err := service.SetApprovalMode(
		context.Background(), testSessionID, testUserID, ApprovalFull, time.Now().UTC(),
	); err != nil {
		t.Fatal(err)
	}
	var note Content
	if err := json.Unmarshal(fake.modeNote, &note); err != nil {
		t.Fatal(err)
	}
	if note.Mode != ApprovalFull || note.Text != "" {
		t.Fatalf("unexpected note: %+v", note)
	}
}

func TestSetApprovalModeRefusesAnUnknownMode(t *testing.T) {
	t.Parallel()

	fake := newFakeStore()
	if _, err := newTestService(fake).SetApprovalMode(
		context.Background(), testSessionID, testUserID, ApprovalMode("yolo"), time.Now().UTC(),
	); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("expected ErrInvalidInput, got %v", err)
	}
	if fake.mode != "" {
		t.Fatal("a refused switch must not reach the store")
	}
}

// A turn is opened by the question that starts it.// A turn is opened by the question that starts it. The sequence keeps running
// across turns, because "I have seen up to entry N" has to mean one thing for
// the whole session.
func TestTurnsShareOneRunningSequence(t *testing.T) {
	t.Parallel()

	fake := newFakeStore()
	service := newTestService(fake)
	now := time.Now().UTC()

	first := startTurn(t, service, "第一个问题")
	if first.Turn != 1 || first.Sequence != 1 || first.Kind != KindInput {
		t.Fatalf("unexpected opening entry: %+v", first)
	}
	appended, err := service.Append(context.Background(), AppendInput{
		SessionID: testSessionID, Kind: KindConclusion,
		Content: Content{Text: "结论"}, OccurredAt: now,
	})
	if err != nil {
		t.Fatal(err)
	}
	if appended.Turn != 1 || appended.Sequence != 2 {
		t.Fatalf("unexpected entry: %+v", appended)
	}
	if _, err := service.FinishTurn(context.Background(), FinishTurnInput{
		SessionID: testSessionID, Status: TurnSucceeded, Now: now,
	}); err != nil {
		t.Fatal(err)
	}

	second := startTurn(t, service, "第二个问题")
	if second.Turn != 2 || second.Sequence != 3 {
		t.Fatalf("the second turn must continue the sequence: %+v", second)
	}
}

func TestSecondQuestionWhileWorkingIsRefused(t *testing.T) {
	t.Parallel()

	fake := newFakeStore()
	service := newTestService(fake)
	startTurn(t, service, "第一个问题")

	_, err := service.StartTurn(context.Background(), StartTurnInput{
		SessionID: testSessionID, InitiatorUserID: testUserID,
		TenantID: testTenantID, ProjectID: testProjectID,
		Content: Content{Text: "插一句"}, Now: time.Now().UTC(),
	})
	if !errors.Is(err, ErrBusy) {
		t.Fatalf("expected ErrBusy, got %v", err)
	}
}

func TestDailyQuotaIsReportedAndBlocksBeforeTurnStarts(t *testing.T) {
	t.Parallel()

	fake := newFakeStore()
	fake.usage = store.AIUsage{Turns: 5, InputTokens: 700, OutputTokens: 300}
	service := NewService(fake, Config{DailyTurnLimit: 5, DailyTokenLimit: 2_000})
	now := time.Date(2026, 8, 30, 15, 0, 0, 0, time.FixedZone("test", 8*60*60))
	quota, err := service.Quota(context.Background(), testUserID, testTenantID, testProjectID, now)
	if err != nil {
		t.Fatal(err)
	}
	if !quota.Exhausted || quota.TurnsUsed != 5 || quota.TokensUsed != 1_000 ||
		quota.PeriodStart.Hour() != 0 || quota.PeriodStart.Location() != time.UTC {
		t.Fatalf("unexpected quota: %+v", quota)
	}

	fake.quotaExceeded = true
	_, err = service.StartTurn(context.Background(), StartTurnInput{
		SessionID: testSessionID, InitiatorUserID: testUserID,
		TenantID: testTenantID, ProjectID: testProjectID,
		Content: Content{Text: "继续诊断"}, Now: now,
	})
	if !errors.Is(err, ErrQuotaExceeded) || len(fake.started) != 0 {
		t.Fatalf("quota start error = %v, started = %d", err, len(fake.started))
	}
}

func TestFeedbackIsBoundedAndEvaluationUsesStoredFacts(t *testing.T) {
	t.Parallel()

	fake := newFakeStore()
	service := newTestService(fake)
	now := time.Now().UTC()
	feedback, err := service.SaveFeedback(context.Background(), FeedbackInput{
		SessionID: testSessionID, Turn: 2, InitiatorUserID: testUserID,
		Rating: "not_helpful", Outcome: "unresolved",
		Reasons: []string{"insufficient_evidence", "incomplete"}, Comment: " 缺少变更前指标 ", Now: now,
	})
	if err != nil {
		t.Fatal(err)
	}
	if feedback.Comment != "缺少变更前指标" || feedback.Turn != 2 {
		t.Fatalf("unexpected feedback: %+v", feedback)
	}
	if _, err := service.SaveFeedback(context.Background(), FeedbackInput{
		SessionID: testSessionID, Turn: 2, InitiatorUserID: testUserID,
		Rating: "not_helpful", Outcome: "unresolved",
		Reasons: []string{"incomplete", "incomplete"}, Now: now,
	}); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("duplicate feedback reasons error = %v", err)
	}

	fake.evaluation = store.AIEvaluation{Turns: 4, Succeeded: 3, Failed: 1,
		Rated: 2, Helpful: 1, Resolved: 1, ToolCalls: 8, Duration: 12 * time.Second,
		FailureCounts: map[string]int64{"model_timeout": 1},
		ReasonCounts:  map[string]int64{"incomplete": 1}}
	evaluation, err := service.Evaluate(context.Background(), EvaluationInput{
		InitiatorUserID: testUserID, TenantID: testTenantID, ProjectID: testProjectID,
		ClusterID: testClusterID, From: now.Add(-30 * 24 * time.Hour), To: now,
	})
	if err != nil {
		t.Fatal(err)
	}
	if evaluation.DurationMS != 12_000 || evaluation.Helpful != 1 ||
		evaluation.FailureCounts["model_timeout"] != 1 {
		t.Fatalf("unexpected evaluation: %+v", evaluation)
	}
}

func TestAppendOutsideATurnIsRefused(t *testing.T) {
	t.Parallel()

	service := newTestService(newFakeStore())
	_, err := service.Append(context.Background(), AppendInput{
		SessionID: testSessionID, Kind: KindConclusion,
		Content: Content{Text: "结论"}, OccurredAt: time.Now().UTC(),
	})
	if !errors.Is(err, ErrIdle) {
		t.Fatalf("expected ErrIdle, got %v", err)
	}
}

// Cluster content is data, never instruction. The mark is applied here rather
// than trusted to the caller, so no writer can produce an unmarked context
// entry by forgetting.
func TestAppendMarksClusterContentUntrusted(t *testing.T) {
	t.Parallel()

	fake := newFakeStore()
	service := newTestService(fake)
	startTurn(t, service, "问题")

	entry, err := service.Append(context.Background(), AppendInput{
		SessionID:  testSessionID,
		Kind:       KindContext,
		Content:    Content{Text: "Back-off restarting failed container"},
		OccurredAt: time.Now().UTC(),
	})
	if err != nil {
		t.Fatal(err)
	}
	if !entry.Content.Untrusted {
		t.Fatal("a context entry must be marked untrusted")
	}
	if !decodeAppended(t, fake, 0).Untrusted {
		t.Fatal("the mark must reach the stored content, not just the return value")
	}
}

func TestAppendBoundsTheBody(t *testing.T) {
	t.Parallel()

	fake := newFakeStore()
	service := newTestService(fake)
	startTurn(t, service, "问题")

	// Multi-byte runes, so a naive cut at a byte offset would leave a broken
	// character behind.
	oversized := strings.Repeat("集群", maxTextBytes)
	entry, err := service.Append(context.Background(), AppendInput{
		SessionID:  testSessionID,
		Kind:       KindModel,
		Content:    Content{Text: oversized, Arguments: strings.Repeat("a", maxArgumentsBytes+10)},
		OccurredAt: time.Now().UTC(),
	})
	if err != nil {
		t.Fatal(err)
	}
	if !entry.Truncated {
		t.Fatal("a body that was cut must say so")
	}
	stored := decodeAppended(t, fake, 0)
	if len(stored.Text) > maxTextBytes || len(stored.Arguments) > maxArgumentsBytes {
		t.Fatalf("body not bounded: %d text bytes, %d argument bytes",
			len(stored.Text), len(stored.Arguments))
	}
	if !strings.HasPrefix(oversized, stored.Text) {
		t.Fatal("truncation must cut on a rune boundary and keep the prefix intact")
	}
	if !fake.appended[0].Truncated {
		t.Fatal("the stored row must carry the truncation mark")
	}
}

func TestAppendRecordsContextCompaction(t *testing.T) {
	t.Parallel()

	fake := newFakeStore()
	service := newTestService(fake)
	startTurn(t, service, "继续排查")
	stats := &Compaction{
		Method: CompactionModelSummary, Trigger: CompactionTriggerPressure,
		BeforeTokens: 200_000, AfterTokens: 42_000, ThresholdTokens: 209_715,
		RetainedTokens: 41_000, ContextWindowTokens: 262_144,
		ShadowedFrom: 1, ShadowedTo: 12,
	}
	entry, err := service.Append(context.Background(), AppendInput{
		SessionID: testSessionID, Kind: KindCompaction,
		Content: Content{Text: "检查点", Compaction: stats}, OccurredAt: time.Now().UTC(),
	})
	if err != nil {
		t.Fatal(err)
	}
	if entry.Content.Compaction == nil || entry.Content.Compaction.AfterTokens != 42_000 {
		t.Fatalf("unexpected compaction entry: %+v", entry)
	}
}

func TestAppendRefusesAnEntryItsKindCannotCarry(t *testing.T) {
	t.Parallel()

	cases := map[string]AppendInput{
		"未知类型":       {Kind: Kind("thinking"), Content: Content{Text: "x"}},
		"模型输出没有正文":   {Kind: KindModel, Content: Content{Text: "  "}},
		"结论没有正文":     {Kind: KindConclusion, Content: Content{Text: "  "}},
		"工具调用没有工具名":  {Kind: KindToolCall, Content: Content{Text: "x"}},
		"工具结果没有工具名":  {Kind: KindToolResult, Content: Content{Text: "x"}},
		"错误没有分类":     {Kind: KindError, Content: Content{Text: "x"}},
		"错误的分类不在词表里": {Kind: KindError, Content: Content{Failure: "something_went_wrong"}},
		"压缩没有统计":     {Kind: KindCompaction},
		// The question is written by the call that opens the turn. A second
		// path to write one would allow a turn with two questions in it.
		"另一条提问": {Kind: KindInput, Content: Content{Text: "再问一次"}},
	}
	for name, input := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			fake := newFakeStore()
			service := newTestService(fake)
			startTurn(t, service, "问题")
			input.SessionID = testSessionID
			input.OccurredAt = time.Now().UTC()
			if _, err := service.Append(context.Background(), input); !errors.Is(err, ErrInvalidInput) {
				t.Fatalf("expected ErrInvalidInput, got %v", err)
			}
			if len(fake.appended) != 0 {
				t.Fatal("a refused entry must not reach the store")
			}
		})
	}
}

func TestFinishTurnRequiresACoherentOutcome(t *testing.T) {
	t.Parallel()

	cases := map[string]FinishTurnInput{
		"未知状态":       {Status: TurnStatus("done")},
		"仍然在跑":       {Status: TurnStatus("working")},
		"失败但没有分类":    {Status: TurnFailed},
		"失败的分类不在词表里": {Status: TurnFailed, Failure: "model_said_no"},
		"成功却带着失败原因":  {Status: TurnSucceeded, Failure: FailureBudgetExceeded},
		"取消却带着失败原因":  {Status: TurnCanceled, Failure: FailureSessionEnded},
	}
	for name, input := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			fake := newFakeStore()
			service := newTestService(fake)
			startTurn(t, service, "问题")
			input.SessionID = testSessionID
			input.Now = time.Now().UTC()
			if _, err := service.FinishTurn(context.Background(), input); !errors.Is(
				err, ErrInvalidInput,
			) {
				t.Fatalf("expected ErrInvalidInput, got %v", err)
			}
			if !fake.working {
				t.Fatal("a refused finish must leave the turn running")
			}
		})
	}
}

func TestFinishTurnAcceptsEveryOutcome(t *testing.T) {
	t.Parallel()

	cases := []FinishTurnInput{
		{Status: TurnSucceeded},
		{Status: TurnCanceled},
		{Status: TurnFailed, Failure: FailureModelTimeout},
		{Status: TurnFailed, Failure: FailurePermissionRevoked},
		{Status: TurnFailed, Failure: FailureBudgetExceeded},
	}
	for _, input := range cases {
		t.Run(string(input.Status)+" "+input.Failure, func(t *testing.T) {
			t.Parallel()

			fake := newFakeStore()
			service := newTestService(fake)
			startTurn(t, service, "问题")
			input.SessionID = testSessionID
			input.Now = time.Now().UTC()
			session, err := service.FinishTurn(context.Background(), input)
			if err != nil {
				t.Fatal(err)
			}
			if session.Status != StatusIdle {
				t.Fatalf("a finished turn leaves the session idle: %+v", session)
			}
			if fake.finished.Status != string(input.Status) ||
				fake.finished.Failure != input.Failure {
				t.Fatalf("stored %+v, want %s/%s", fake.finished, input.Status, input.Failure)
			}
		})
	}
}

func TestTrajectoryResumesAfterASequence(t *testing.T) {
	t.Parallel()

	occurred := time.Now().UTC()
	fake := newFakeStore()
	fake.events = []store.AISessionEvent{
		{
			Sequence: 1, Turn: 1, Kind: string(KindSystem), OccurredAt: occurred,
			Content: []byte(`{"text":"rules"}`),
		},
		{
			Sequence: 2, Turn: 1, Kind: string(KindToolCall), OccurredAt: occurred,
			Content: []byte(`{"tool":"pod.logs","target":{"cluster":"c","namespace":"n"},"authorized":true}`),
			// A duration the store reports in milliseconds must come back as a
			// duration, not as a number nobody can interpret.
			Duration: 1500 * time.Millisecond,
		},
	}
	entries, err := newTestService(fake).Trajectory(context.Background(), TrajectoryQuery{
		SessionID:       testSessionID,
		InitiatorUserID: testUserID,
		AfterSequence:   1,
		Now:             occurred,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].Sequence != 2 {
		t.Fatalf("resume returned %d entries: %+v", len(entries), entries)
	}
	entry := entries[0]
	if entry.Kind != KindToolCall || entry.Content.Tool != "pod.logs" || entry.Turn != 1 {
		t.Fatalf("entry not decoded: %+v", entry)
	}
	if entry.Content.Target == nil || entry.Content.Target.Namespace != "n" {
		t.Fatalf("target not decoded: %+v", entry.Content)
	}
	if entry.Content.Authorized == nil || !*entry.Content.Authorized {
		t.Fatal("the authorization decision must survive the round trip")
	}
	if entry.Duration != 1500*time.Millisecond {
		t.Fatalf("duration %s", entry.Duration)
	}
}

func TestGetReportsNotFoundForSomebodyElsesSession(t *testing.T) {
	t.Parallel()

	fake := newFakeStore()
	fake.notFound = true
	_, err := newTestService(fake).Get(
		context.Background(), testSessionID, testOtherUser, time.Now().UTC(),
	)
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("expected ErrNotFound, got %v", err)
	}
}

func TestRecoverInterruptedEndsTurnsWithNoProcessBehindThem(t *testing.T) {
	t.Parallel()

	fake := newFakeStore()
	service := newTestService(fake)
	startTurn(t, service, "问题")

	recovered, err := service.RecoverInterrupted(context.Background(), time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	if recovered != 1 {
		t.Fatalf("recovered %d turns", recovered)
	}
	if fake.interrupted.Failure != FailureInterrupted {
		t.Fatalf("unexpected classification: %q", fake.interrupted.Failure)
	}
	// The entry written into the trail carries the classification and no prose:
	// a display string in the database is one nobody can translate later.
	var content Content
	if err := json.Unmarshal(fake.interrupted.Content, &content); err != nil {
		t.Fatal(err)
	}
	if content.Failure != FailureInterrupted || content.Text != "" {
		t.Fatalf("unexpected interruption entry: %+v", content)
	}
}

func TestAppSessionManagementKeepsTheInitiatorBoundary(t *testing.T) {
	t.Parallel()

	fake := newFakeStore()
	service := newTestService(fake)
	now := time.Now().UTC()

	found, err := service.Search(context.Background(), SearchInput{
		InitiatorUserID: testUserID,
		TenantID:        testTenantID,
		ProjectID:       testProjectID,
		ClusterID:       testClusterID,
		Query:           "  控制面  ",
		Archived:        true,
		Now:             now,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(found) != 1 || found[0].ArchivedAt == nil {
		t.Fatalf("archived search result not decoded: %+v", found)
	}
	if fake.searched.InitiatorUserID != testUserID || fake.searched.TenantID != testTenantID ||
		fake.searched.ProjectID != testProjectID || fake.searched.ClusterID != testClusterID ||
		fake.searched.Query != "控制面" ||
		!fake.searched.Archived {
		t.Fatalf("search scope was not preserved: %+v", fake.searched)
	}

	renamed, err := service.Rename(
		context.Background(), testSessionID, testUserID, "  控制面\n异常  ", now,
	)
	if err != nil {
		t.Fatal(err)
	}
	if renamed.Title != "控制面 异常" || fake.title != renamed.Title {
		t.Fatalf("title was not normalized: %+v", renamed)
	}

	archived, err := service.SetArchived(
		context.Background(), testSessionID, testUserID, true, now,
	)
	if err != nil {
		t.Fatal(err)
	}
	if archived.ArchivedAt == nil || !fake.archived {
		t.Fatalf("archive state was not returned: %+v", archived)
	}
	if err := service.Delete(context.Background(), testSessionID, testUserID, now); err != nil {
		t.Fatal(err)
	}
	if !fake.deleted {
		t.Fatal("archived session was not deleted")
	}
}

func TestDeleteRequiresArchiveFirst(t *testing.T) {
	t.Parallel()

	fake := newFakeStore()
	err := newTestService(fake).Delete(
		context.Background(), testSessionID, testUserID, time.Now().UTC(),
	)
	if !errors.Is(err, ErrNotArchived) {
		t.Fatalf("Delete() error = %v, want ErrNotArchived", err)
	}
	if fake.deleted {
		t.Fatal("active session must not be deleted")
	}
}

func TestTextAttachmentsCanBeAddedListedAndDeleted(t *testing.T) {
	t.Parallel()

	fake := newFakeStore()
	service := newTestService(fake)
	now := time.Now().UTC()
	created, err := service.AddAttachment(context.Background(), AttachmentInput{
		SessionID: testSessionID, InitiatorUserID: testUserID,
		Name: "  values.yaml  ", MediaType: "application/yaml",
		Content: "replicas: 3", Now: now,
	})
	if err != nil {
		t.Fatal(err)
	}
	if created.Name != "values.yaml" || created.Content != "replicas: 3" {
		t.Fatalf("attachment was not normalized: %+v", created)
	}

	attachments, err := service.Attachments(
		context.Background(), testSessionID, testUserID, now,
	)
	if err != nil {
		t.Fatal(err)
	}
	if len(attachments) != 1 || attachments[0].ID != created.ID {
		t.Fatalf("attachment list mismatch: %+v", attachments)
	}
	if err := service.DeleteAttachment(
		context.Background(), testSessionID, created.ID, testUserID,
	); err != nil {
		t.Fatal(err)
	}
	if len(fake.attachments) != 0 {
		t.Fatalf("attachment was not deleted: %+v", fake.attachments)
	}

	_, err = service.AddAttachment(context.Background(), AttachmentInput{
		SessionID: testSessionID, InitiatorUserID: testUserID,
		Name: "dump.bin", MediaType: "application/octet-stream", Content: "binary", Now: now,
	})
	if !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("binary attachment should be rejected, got %v", err)
	}
}
