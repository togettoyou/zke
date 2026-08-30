package store

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

var (
	// ErrAISessionNotFound reports a session that does not exist, has aged out,
	// or belongs to somebody else. One error for all three on purpose: the
	// reads are scoped by initiator in SQL, and telling a caller that a session
	// they may not read exists would be the fact they were refused.
	ErrAISessionNotFound = errors.New("AIOps session not found")
	// ErrAISessionBusy reports a second turn started while one is running. One
	// session runs one turn at a time; the trail would otherwise interleave two
	// lines of work under one sequence.
	ErrAISessionBusy = errors.New("AIOps session already has a turn running")
	// ErrAISessionIdle reports a write with no turn running. The trail is
	// append-only and every entry belongs to a turn.
	ErrAISessionIdle = errors.New("AIOps session has no turn running")
	// ErrAISessionNotArchived prevents active conversations from being deleted.
	// Archiving is the explicit first step and keeps accidental deletion out of
	// the normal session view.
	ErrAISessionNotArchived = errors.New("AIOps session must be archived before deletion")
	// ErrAIQuotaExceeded is returned before opening a new turn. The model call
	// has not started and no input entry has been appended when this is seen.
	ErrAIQuotaExceeded = errors.New("AIOps daily quota exceeded")
)

// AISession is one session's row. Entries live in their own table and are never
// returned as part of this.
type AISession struct {
	ID              string
	InitiatorUserID string
	TenantID        string
	ProjectID       string
	ClusterID       string
	Title           string
	Status          string
	// ApprovalMode is how far this session may go without asking.
	ApprovalMode    string
	CurrentTurn     int32
	LastTurnStatus  string
	LastTurnFailure string
	CreatedAt       time.Time
	LastActivityAt  time.Time
	ArchivedAt      *time.Time
}

// AISessionEvent is one entry of a trail.
//
// Content is the encoded body rather than a decoded shape: which fields are
// meaningful depends on Kind, and that mapping belongs to the package that
// writes and renders them, not to the one that stores rows.
type AISessionEvent struct {
	Sequence   int32
	Turn       int32
	Kind       string
	Content    []byte
	Truncated  bool
	OccurredAt time.Time
	Duration   time.Duration
}

type CreateAISessionParams struct {
	ID              string
	InitiatorUserID string
	TenantID        string
	ProjectID       string
	ClusterID       string
	Title           string
	ApprovalMode    string
	Now             time.Time
	// RetentionCutoff is the moment before which sessions are reclaimed. Passed
	// in rather than stored per row so that changing the window does not mean
	// rewriting every session.
	RetentionCutoff time.Time
}

// StartAITurnParams opens a turn and writes its first entry in one statement.
// The two are one act: a turn with no question in it is not a turn.
type StartAITurnParams struct {
	SessionID       string
	InitiatorUserID string
	TenantID        string
	ProjectID       string
	Content         []byte
	Truncated       bool
	OccurredAt      time.Time
	TurnLimit       int64
	TokenLimit      int64
}

type AppendAISessionEventParams struct {
	SessionID  string
	Kind       string
	Content    []byte
	Truncated  bool
	OccurredAt time.Time
	Duration   time.Duration
}

type FinishAITurnParams struct {
	SessionID string
	Status    string
	Failure   string
	Now       time.Time
}

// InterruptAITurnsParams ends every turn whose Server process is gone and
// writes one error entry into each trail saying so.
type InterruptAITurnsParams struct {
	Failure string
	Content []byte
	Now     time.Time
}

type SearchAISessionsParams struct {
	InitiatorUserID string
	TenantID        string
	ProjectID       string
	ClusterID       string
	RetentionCutoff time.Time
	Query           string
	Archived        bool
	Limit           int
}

type AISessionAttachment struct {
	ID        string
	SessionID string
	Name      string
	MediaType string
	Content   string
	CreatedAt time.Time
}

type CreateAISessionAttachmentParams struct {
	ID              string
	SessionID       string
	InitiatorUserID string
	Name            string
	MediaType       string
	Content         string
	CreatedAt       time.Time
	RetentionCutoff time.Time
}

type AIUsage struct {
	PeriodStart  time.Time
	PeriodEnd    time.Time
	Turns        int64
	InputTokens  int64
	OutputTokens int64
}

type AITurnFeedback struct {
	SessionID string
	Turn      int32
	Rating    string
	Outcome   string
	Reasons   []string
	Comment   string
	CreatedAt time.Time
	UpdatedAt time.Time
}

type UpsertAITurnFeedbackParams struct {
	SessionID       string
	Turn            int32
	InitiatorUserID string
	Rating          string
	Outcome         string
	Reasons         []string
	Comment         string
	Now             time.Time
}

type AIEvaluationQuery struct {
	InitiatorUserID string
	TenantID        string
	ProjectID       string
	ClusterID       string
	From            time.Time
	To              time.Time
}

type AIEvaluation struct {
	Turns         int64
	Succeeded     int64
	Failed        int64
	Canceled      int64
	Rated         int64
	Helpful       int64
	Resolved      int64
	ToolCalls     int64
	Duration      time.Duration
	FailureCounts map[string]int64
	ReasonCounts  map[string]int64
}

type AISessionStore struct {
	pool *pgxpool.Pool
}

func NewAISessionStore(pool *pgxpool.Pool) *AISessionStore {
	return &AISessionStore{pool: pool}
}

const aiSessionColumns = `
    id::text,
    initiator_user_id::text,
    tenant_id::text,
    project_id::text,
    cluster_id::text,
    title,
    status,
    approval_mode,
    current_turn,
    last_turn_status,
    last_turn_failure,
    created_at,
    last_activity_at,
    archived_at`

func scanAISession(row pgx.Row) (AISession, error) {
	var session AISession
	err := row.Scan(
		&session.ID,
		&session.InitiatorUserID,
		&session.TenantID,
		&session.ProjectID,
		&session.ClusterID,
		&session.Title,
		&session.Status,
		&session.ApprovalMode,
		&session.CurrentTurn,
		&session.LastTurnStatus,
		&session.LastTurnFailure,
		&session.CreatedAt,
		&session.LastActivityAt,
		&session.ArchivedAt,
	)
	if err != nil {
		return AISession{}, err
	}
	return session, nil
}

// CreateAISession opens a session with no turns in it yet.
//
// Aged-out sessions are reclaimed here rather than by a sweeper, the same way
// terminal recordings are: the write path is the one place guaranteed to run in
// a deployment that never opens the history.
func (store *AISessionStore) CreateAISession(
	ctx context.Context,
	input CreateAISessionParams,
) (AISession, error) {
	_ = store.deleteAgedOutAISessions(ctx, input.RetentionCutoff)
	session, err := scanAISession(store.pool.QueryRow(ctx, `
INSERT INTO ai_sessions (
    id, initiator_user_id, tenant_id, project_id, cluster_id,
    title, status, approval_mode, created_at, last_activity_at
) VALUES ($1::uuid, $2::uuid, $3::uuid, $4::uuid, $5::uuid, $6, 'idle', $7, $8, $8)
RETURNING `+aiSessionColumns,
		input.ID,
		input.InitiatorUserID,
		input.TenantID,
		input.ProjectID,
		input.ClusterID,
		input.Title,
		input.ApprovalMode,
		input.Now,
	))
	if err != nil {
		return AISession{}, fmt.Errorf("create AIOps session: %w", err)
	}
	return session, nil
}

// SetAISessionApprovalMode changes how far a session may go without asking,
// and notes the change in the trail when a turn is running.
//
// The operator switches whenever they like — including mid-turn, which is the
// point of having the control in the composer. A turn's record stays honest
// because the switch is appended as an entry rather than refused: the trail
// then says which mode each part of the turn ran under, instead of only which
// mode is set now.
//
// The entry and the state change are one statement, so a switch cannot be
// recorded without happening or happen without being recorded.
func (store *AISessionStore) SetAISessionApprovalMode(
	ctx context.Context,
	sessionID string,
	initiatorUserID string,
	mode string,
	noteContent []byte,
	now time.Time,
) (AISession, error) {
	session, err := scanAISession(store.pool.QueryRow(ctx, `
WITH switched AS (
    UPDATE ai_sessions
    SET approval_mode = $3,
        next_sequence = next_sequence + (CASE WHEN status = 'working' THEN 1 ELSE 0 END),
        last_activity_at = $4
    WHERE id = $1::uuid AND initiator_user_id = $2::uuid
    RETURNING id, initiator_user_id, tenant_id, project_id, cluster_id,
              title, status, approval_mode, current_turn,
              next_sequence, last_turn_status, last_turn_failure,
              created_at, last_activity_at, archived_at
), noted AS (
    INSERT INTO ai_session_events (
        session_id, sequence, turn, kind, content, occurred_at
    )
    SELECT id, next_sequence - 1, current_turn, 'system', $5::jsonb, $4
    FROM switched
    WHERE status = 'working'
)
SELECT `+aiSessionColumns+`
FROM switched`,
		sessionID, initiatorUserID, mode, now, noteContent,
	))
	if errors.Is(err, pgx.ErrNoRows) {
		return AISession{}, ErrAISessionNotFound
	}
	if err != nil {
		return AISession{}, fmt.Errorf("set AIOps session approval mode: %w", err)
	}
	return session, nil
}

// StartAITurn opens the next turn and writes the question that opened it.
//
// Both the turn number and the entry's sequence are allocated by this
// statement, so a second turn racing the first is refused by the row's state
// rather than by a check somebody remembered to write.
func (store *AISessionStore) StartAITurn(
	ctx context.Context,
	input StartAITurnParams,
) (AISessionEvent, error) {
	tx, err := store.pool.Begin(ctx)
	if err != nil {
		return AISessionEvent{}, fmt.Errorf("begin AIOps turn: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	if input.TurnLimit > 0 || input.TokenLimit > 0 {
		periodStart := input.OccurredAt.UTC().Truncate(24 * time.Hour)
		// Admission for one user and Project is serialized. Otherwise two
		// sessions can both observe the last remaining turn and spend it.
		if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1, 0))`,
			input.InitiatorUserID+":"+input.ProjectID+":"+periodStart.Format(time.DateOnly)); err != nil {
			return AISessionEvent{}, fmt.Errorf("lock AIOps quota: %w", err)
		}
		usage, usageErr := queryAIUsage(ctx, tx, input.InitiatorUserID, input.TenantID,
			input.ProjectID, periodStart, periodStart.Add(24*time.Hour))
		if usageErr != nil {
			return AISessionEvent{}, usageErr
		}
		if (input.TurnLimit > 0 && usage.Turns >= input.TurnLimit) ||
			(input.TokenLimit > 0 && usage.InputTokens+usage.OutputTokens >= input.TokenLimit) {
			return AISessionEvent{}, ErrAIQuotaExceeded
		}
	}

	var event AISessionEvent
	err = tx.QueryRow(ctx, `
WITH opened AS (
    UPDATE ai_sessions
    SET status = 'working',
        current_turn = current_turn + 1,
        next_sequence = next_sequence + 1,
        last_turn_status = '',
        last_turn_failure = '',
        last_activity_at = $4
    WHERE id = $1::uuid
      AND initiator_user_id = $5::uuid
      AND tenant_id = $6::uuid
      AND project_id = $7::uuid
      AND status = 'idle' AND archived_at IS NULL
    RETURNING current_turn AS turn, next_sequence - 1 AS sequence
), recorded AS (
    INSERT INTO ai_turn_runs (session_id, turn, started_at, status)
    SELECT $1::uuid, opened.turn, $4, 'running' FROM opened
    RETURNING turn
)
INSERT INTO ai_session_events (
    session_id, sequence, turn, kind, content, truncated, occurred_at
)
SELECT $1::uuid, opened.sequence, opened.turn, 'input', $2::jsonb, $3, $4
FROM opened JOIN recorded USING (turn)
RETURNING sequence, turn`,
		input.SessionID,
		input.Content,
		input.Truncated,
		input.OccurredAt,
		input.InitiatorUserID,
		input.TenantID,
		input.ProjectID,
	).Scan(&event.Sequence, &event.Turn)
	if errors.Is(err, pgx.ErrNoRows) {
		return AISessionEvent{}, store.classifyMissingSession(ctx, input.SessionID, ErrAISessionBusy)
	}
	if err != nil {
		return AISessionEvent{}, fmt.Errorf("start AIOps turn: %w", err)
	}
	event.Kind = "input"
	event.Content = input.Content
	event.Truncated = input.Truncated
	event.OccurredAt = input.OccurredAt
	if err := tx.Commit(ctx); err != nil {
		return AISessionEvent{}, fmt.Errorf("commit AIOps turn: %w", err)
	}
	return event, nil
}

// AppendAISessionEvent writes one entry into the running turn and reports the
// sequence and turn it was given.
func (store *AISessionStore) AppendAISessionEvent(
	ctx context.Context,
	input AppendAISessionEventParams,
) (AISessionEvent, error) {
	var event AISessionEvent
	err := store.pool.QueryRow(ctx, `
WITH allocated AS (
    UPDATE ai_sessions
    SET next_sequence = next_sequence + 1,
        last_activity_at = $6
    WHERE id = $1::uuid AND status = 'working'
    RETURNING current_turn AS turn, next_sequence - 1 AS sequence
)
INSERT INTO ai_session_events (
    session_id, sequence, turn, kind, content, truncated, occurred_at, duration_ms
)
SELECT $1::uuid, allocated.sequence, allocated.turn, $2, $3::jsonb, $4, $5, $7
FROM allocated
RETURNING sequence, turn`,
		input.SessionID,
		input.Kind,
		input.Content,
		input.Truncated,
		input.OccurredAt,
		input.OccurredAt,
		input.Duration.Milliseconds(),
	).Scan(&event.Sequence, &event.Turn)
	if errors.Is(err, pgx.ErrNoRows) {
		return AISessionEvent{}, store.classifyMissingSession(ctx, input.SessionID, ErrAISessionIdle)
	}
	if err != nil {
		return AISessionEvent{}, fmt.Errorf("append AIOps session event: %w", err)
	}
	event.Kind = input.Kind
	event.Content = input.Content
	event.Truncated = input.Truncated
	event.OccurredAt = input.OccurredAt
	event.Duration = input.Duration
	return event, nil
}

// FinishAITurn closes the running turn and records how it ended. A turn ends
// once: the second call matches no row.
func (store *AISessionStore) FinishAITurn(
	ctx context.Context,
	input FinishAITurnParams,
) (AISession, error) {
	session, err := scanAISession(store.pool.QueryRow(ctx, `
WITH finished AS (
    UPDATE ai_turn_runs AS turn
    SET status = $2, failure = $3, finished_at = $4
    FROM ai_sessions AS session
    WHERE turn.session_id = $1::uuid
      AND turn.session_id = session.id
      AND turn.turn = session.current_turn
      AND turn.status = 'running'
    RETURNING turn.session_id
)
UPDATE ai_sessions
SET status = 'idle',
    last_turn_status = $2,
    last_turn_failure = $3,
    last_activity_at = $4
WHERE id = $1::uuid AND status = 'working'
  AND EXISTS (SELECT 1 FROM finished WHERE finished.session_id = ai_sessions.id)
RETURNING `+aiSessionColumns,
		input.SessionID,
		input.Status,
		input.Failure,
		input.Now,
	))
	if errors.Is(err, pgx.ErrNoRows) {
		return AISession{}, store.classifyMissingSession(ctx, input.SessionID, ErrAISessionIdle)
	}
	if err != nil {
		return AISession{}, fmt.Errorf("finish AIOps turn: %w", err)
	}
	return session, nil
}

// GetAISessionForInitiator reads one session for the person who started it.
//
// The initiator is part of the predicate rather than checked afterwards: this
// is the ownership rule for a trail. Current cluster authorization is a
// separate check performed when session APIs expose cluster-derived bodies.
func (store *AISessionStore) GetAISessionForInitiator(
	ctx context.Context,
	sessionID string,
	initiatorUserID string,
	retentionCutoff time.Time,
) (AISession, error) {
	session, err := scanAISession(store.pool.QueryRow(ctx, `SELECT `+aiSessionColumns+`
FROM ai_sessions
WHERE id = $1::uuid
  AND initiator_user_id = $2::uuid
  AND last_activity_at > $3`, sessionID, initiatorUserID, retentionCutoff))
	if errors.Is(err, pgx.ErrNoRows) {
		return AISession{}, ErrAISessionNotFound
	}
	if err != nil {
		return AISession{}, fmt.Errorf("get AIOps session: %w", err)
	}
	return session, nil
}

// ListAISessionsForInitiator reads one person's own sessions, most recently
// used first.
func (store *AISessionStore) ListAISessionsForInitiator(
	ctx context.Context,
	initiatorUserID string,
	tenantID string,
	projectID string,
	clusterID string,
	retentionCutoff time.Time,
	limit int,
) ([]AISession, error) {
	rows, err := store.pool.Query(ctx, `SELECT `+aiSessionColumns+`
FROM ai_sessions
WHERE initiator_user_id = $1::uuid
  AND tenant_id = $2::uuid
  AND project_id = $3::uuid
  AND cluster_id = $4::uuid
  AND last_activity_at > $5
  AND archived_at IS NULL
ORDER BY last_activity_at DESC, id DESC
LIMIT $6`, initiatorUserID, tenantID, projectID, clusterID, retentionCutoff, limit)
	if err != nil {
		return nil, fmt.Errorf("list AIOps sessions: %w", err)
	}
	defer rows.Close()
	sessions := make([]AISession, 0)
	for rows.Next() {
		session, err := scanAISession(rows)
		if err != nil {
			return nil, fmt.Errorf("scan AIOps session: %w", err)
		}
		sessions = append(sessions, session)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate AIOps sessions: %w", err)
	}
	return sessions, nil
}

func (store *AISessionStore) SearchAISessionsForInitiator(
	ctx context.Context,
	input SearchAISessionsParams,
) ([]AISession, error) {
	rows, err := store.pool.Query(ctx, `SELECT `+aiSessionColumns+`
FROM ai_sessions
WHERE initiator_user_id = $1::uuid
  AND tenant_id = $2::uuid
  AND project_id = $3::uuid
  AND cluster_id = $4::uuid
  AND last_activity_at > $5
  AND position(lower($6) in lower(title)) > 0
  AND (($7 AND archived_at IS NOT NULL) OR (NOT $7 AND archived_at IS NULL))
ORDER BY last_activity_at DESC, id DESC
LIMIT $8`, input.InitiatorUserID, input.TenantID, input.ProjectID, input.ClusterID, input.RetentionCutoff,
		input.Query, input.Archived, input.Limit)
	if err != nil {
		return nil, fmt.Errorf("search AIOps sessions: %w", err)
	}
	defer rows.Close()
	result := make([]AISession, 0)
	for rows.Next() {
		session, err := scanAISession(rows)
		if err != nil {
			return nil, fmt.Errorf("scan searched AIOps session: %w", err)
		}
		result = append(result, session)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate searched AIOps sessions: %w", err)
	}
	return result, nil
}

func (store *AISessionStore) UpdateAISessionTitle(
	ctx context.Context, sessionID, initiatorUserID, title string, now, retentionCutoff time.Time,
) (AISession, error) {
	session, err := scanAISession(store.pool.QueryRow(ctx, `
UPDATE ai_sessions
SET title = $3, last_activity_at = $4
WHERE id = $1::uuid AND initiator_user_id = $2::uuid
  AND last_activity_at > $5
RETURNING `+aiSessionColumns, sessionID, initiatorUserID, title, now, retentionCutoff))
	if errors.Is(err, pgx.ErrNoRows) {
		return AISession{}, ErrAISessionNotFound
	}
	if err != nil {
		return AISession{}, fmt.Errorf("update AIOps session title: %w", err)
	}
	return session, nil
}

func (store *AISessionStore) SetAISessionArchived(
	ctx context.Context, sessionID, initiatorUserID string, archived bool, now, retentionCutoff time.Time,
) (AISession, error) {
	session, err := scanAISession(store.pool.QueryRow(ctx, `
UPDATE ai_sessions
SET archived_at = CASE WHEN $3 THEN $4::timestamptz ELSE NULL END,
    last_activity_at = $4::timestamptz
WHERE id = $1::uuid AND initiator_user_id = $2::uuid
  AND last_activity_at > $5 AND status = 'idle'
RETURNING `+aiSessionColumns, sessionID, initiatorUserID, archived, now, retentionCutoff))
	if errors.Is(err, pgx.ErrNoRows) {
		return AISession{}, store.classifyOwnedSessionState(
			ctx, sessionID, initiatorUserID, retentionCutoff, ErrAISessionBusy,
		)
	}
	if err != nil {
		return AISession{}, fmt.Errorf("set AIOps session archived: %w", err)
	}
	return session, nil
}

// DeleteAISessionForInitiator permanently removes one archived session. The
// event trail and attachments follow through their ON DELETE CASCADE links.
// An active session never matches this statement, even if a caller bypasses
// the Console and calls the endpoint directly.
func (store *AISessionStore) DeleteAISessionForInitiator(
	ctx context.Context, sessionID, initiatorUserID string, retentionCutoff time.Time,
) error {
	command, err := store.pool.Exec(ctx, `
DELETE FROM ai_sessions
WHERE id = $1::uuid AND initiator_user_id = $2::uuid
  AND last_activity_at > $3 AND archived_at IS NOT NULL AND status = 'idle'`,
		sessionID, initiatorUserID, retentionCutoff,
	)
	if err != nil {
		return fmt.Errorf("delete archived AIOps session: %w", err)
	}
	if command.RowsAffected() == 0 {
		return store.classifyOwnedSessionState(
			ctx, sessionID, initiatorUserID, retentionCutoff, ErrAISessionNotArchived,
		)
	}
	return nil
}

func (store *AISessionStore) classifyOwnedSessionState(
	ctx context.Context,
	sessionID, initiatorUserID string,
	retentionCutoff time.Time,
	stateError error,
) error {
	var exists bool
	if err := store.pool.QueryRow(ctx, `
SELECT EXISTS (
    SELECT 1 FROM ai_sessions
    WHERE id = $1::uuid AND initiator_user_id = $2::uuid AND last_activity_at > $3
)`, sessionID, initiatorUserID, retentionCutoff).Scan(&exists); err != nil {
		return fmt.Errorf("check owned AIOps session state: %w", err)
	}
	if !exists {
		return ErrAISessionNotFound
	}
	return stateError
}

func (store *AISessionStore) CreateAISessionAttachment(
	ctx context.Context, input CreateAISessionAttachmentParams,
) (AISessionAttachment, error) {
	var attachment AISessionAttachment
	err := store.pool.QueryRow(ctx, `
INSERT INTO ai_session_attachments (id, session_id, name, media_type, content, created_at)
SELECT $1::uuid, session.id, $4, $5, $6, $7
FROM ai_sessions AS session
WHERE session.id = $2::uuid AND session.initiator_user_id = $3::uuid
  AND session.last_activity_at > $8 AND session.archived_at IS NULL
RETURNING id::text, session_id::text, name, media_type, content, created_at`,
		input.ID, input.SessionID, input.InitiatorUserID, input.Name, input.MediaType,
		input.Content, input.CreatedAt, input.RetentionCutoff,
	).Scan(&attachment.ID, &attachment.SessionID, &attachment.Name, &attachment.MediaType,
		&attachment.Content, &attachment.CreatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return AISessionAttachment{}, ErrAISessionNotFound
	}
	if err != nil {
		return AISessionAttachment{}, fmt.Errorf("create AIOps attachment: %w", err)
	}
	return attachment, nil
}

func (store *AISessionStore) ListAISessionAttachmentsForInitiator(
	ctx context.Context, sessionID, initiatorUserID string, retentionCutoff time.Time,
) ([]AISessionAttachment, error) {
	rows, err := store.pool.Query(ctx, `
SELECT attachment.id::text, attachment.session_id::text, attachment.name,
       attachment.media_type, attachment.content, attachment.created_at
FROM ai_session_attachments AS attachment
JOIN ai_sessions AS session ON session.id = attachment.session_id
WHERE attachment.session_id = $1::uuid AND session.initiator_user_id = $2::uuid
  AND session.last_activity_at > $3
ORDER BY attachment.created_at, attachment.id`, sessionID, initiatorUserID, retentionCutoff)
	if err != nil {
		return nil, fmt.Errorf("list AIOps attachments: %w", err)
	}
	defer rows.Close()
	result := make([]AISessionAttachment, 0)
	for rows.Next() {
		var attachment AISessionAttachment
		if err := rows.Scan(&attachment.ID, &attachment.SessionID, &attachment.Name,
			&attachment.MediaType, &attachment.Content, &attachment.CreatedAt); err != nil {
			return nil, fmt.Errorf("scan AIOps attachment: %w", err)
		}
		result = append(result, attachment)
	}
	return result, rows.Err()
}

func (store *AISessionStore) DeleteAISessionAttachmentForInitiator(
	ctx context.Context, sessionID, attachmentID, initiatorUserID string,
) error {
	command, err := store.pool.Exec(ctx, `
DELETE FROM ai_session_attachments AS attachment
USING ai_sessions AS session
WHERE attachment.id = $2::uuid AND attachment.session_id = $1::uuid
  AND session.id = attachment.session_id AND session.initiator_user_id = $3::uuid
  AND session.status = 'idle'`, sessionID, attachmentID, initiatorUserID)
	if err != nil {
		return fmt.Errorf("delete AIOps attachment: %w", err)
	}
	if command.RowsAffected() == 0 {
		return ErrAISessionNotFound
	}
	return nil
}

type aiUsageQuerier interface {
	QueryRow(context.Context, string, ...any) pgx.Row
}

func queryAIUsage(
	ctx context.Context,
	querier aiUsageQuerier,
	initiatorUserID, tenantID, projectID string,
	periodStart, periodEnd time.Time,
) (AIUsage, error) {
	usage := AIUsage{PeriodStart: periodStart, PeriodEnd: periodEnd}
	err := querier.QueryRow(ctx, `
SELECT COUNT(DISTINCT (turn.session_id, turn.turn)),
       COALESCE(SUM(
           CASE WHEN event.kind = 'model'
               THEN COALESCE(NULLIF(event.content #>> '{tokens,input}', ''), '0')::bigint
               ELSE 0 END
       ), 0),
       COALESCE(SUM(
           CASE WHEN event.kind = 'model'
               THEN COALESCE(NULLIF(event.content #>> '{tokens,output}', ''), '0')::bigint
               ELSE 0 END
       ), 0)
FROM ai_turn_runs AS turn
JOIN ai_sessions AS session ON session.id = turn.session_id
LEFT JOIN ai_session_events AS event
  ON event.session_id = turn.session_id AND event.turn = turn.turn
WHERE session.initiator_user_id = $1::uuid
  AND session.tenant_id = $2::uuid
  AND session.project_id = $3::uuid
  AND turn.started_at >= $4 AND turn.started_at < $5`,
		initiatorUserID, tenantID, projectID, periodStart, periodEnd,
	).Scan(&usage.Turns, &usage.InputTokens, &usage.OutputTokens)
	if err != nil {
		return AIUsage{}, fmt.Errorf("read AIOps quota usage: %w", err)
	}
	return usage, nil
}

func (store *AISessionStore) GetAIUsage(
	ctx context.Context,
	initiatorUserID, tenantID, projectID string,
	periodStart, periodEnd time.Time,
) (AIUsage, error) {
	return queryAIUsage(ctx, store.pool, initiatorUserID, tenantID, projectID, periodStart, periodEnd)
}

func (store *AISessionStore) UpsertAITurnFeedback(
	ctx context.Context,
	input UpsertAITurnFeedbackParams,
) (AITurnFeedback, error) {
	var feedback AITurnFeedback
	err := store.pool.QueryRow(ctx, `
INSERT INTO ai_turn_feedback (
    session_id, turn, initiator_user_id, rating, outcome, reasons, comment,
    created_at, updated_at
)
SELECT $1::uuid, $2, $3::uuid, $4, $5, $6::text[], $7, $8, $8
FROM ai_sessions AS session
JOIN ai_turn_runs AS turn ON turn.session_id = session.id AND turn.turn = $2
WHERE session.id = $1::uuid
  AND session.initiator_user_id = $3::uuid
  AND turn.status = 'succeeded'
ON CONFLICT (session_id, turn) DO UPDATE
SET rating = EXCLUDED.rating,
    outcome = EXCLUDED.outcome,
    reasons = EXCLUDED.reasons,
    comment = EXCLUDED.comment,
    updated_at = EXCLUDED.updated_at
RETURNING session_id::text, turn, rating, outcome, reasons, comment, created_at, updated_at`,
		input.SessionID, input.Turn, input.InitiatorUserID, input.Rating,
		input.Outcome, input.Reasons, input.Comment, input.Now,
	).Scan(&feedback.SessionID, &feedback.Turn, &feedback.Rating, &feedback.Outcome,
		&feedback.Reasons, &feedback.Comment, &feedback.CreatedAt, &feedback.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return AITurnFeedback{}, ErrAISessionNotFound
	}
	if err != nil {
		return AITurnFeedback{}, fmt.Errorf("save AIOps turn feedback: %w", err)
	}
	return feedback, nil
}

func (store *AISessionStore) GetAITurnFeedback(
	ctx context.Context,
	sessionID string,
	turn int32,
	initiatorUserID string,
) (AITurnFeedback, error) {
	var feedback AITurnFeedback
	err := store.pool.QueryRow(ctx, `
SELECT feedback.session_id::text, feedback.turn, feedback.rating,
       feedback.outcome, feedback.reasons, feedback.comment,
       feedback.created_at, feedback.updated_at
FROM ai_turn_feedback AS feedback
JOIN ai_sessions AS session ON session.id = feedback.session_id
WHERE feedback.session_id = $1::uuid AND feedback.turn = $2
  AND session.initiator_user_id = $3::uuid`,
		sessionID, turn, initiatorUserID,
	).Scan(&feedback.SessionID, &feedback.Turn, &feedback.Rating, &feedback.Outcome,
		&feedback.Reasons, &feedback.Comment, &feedback.CreatedAt, &feedback.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return AITurnFeedback{}, ErrAISessionNotFound
	}
	if err != nil {
		return AITurnFeedback{}, fmt.Errorf("read AIOps turn feedback: %w", err)
	}
	return feedback, nil
}

func (store *AISessionStore) EvaluateAI(
	ctx context.Context,
	query AIEvaluationQuery,
) (AIEvaluation, error) {
	result := AIEvaluation{FailureCounts: map[string]int64{}, ReasonCounts: map[string]int64{}}
	var durationMilliseconds int64
	err := store.pool.QueryRow(ctx, `
SELECT COUNT(*),
       COUNT(*) FILTER (WHERE turn.status = 'succeeded'),
       COUNT(*) FILTER (WHERE turn.status = 'failed'),
       COUNT(*) FILTER (WHERE turn.status = 'canceled'),
       COUNT(feedback.turn),
       COUNT(*) FILTER (WHERE feedback.rating = 'helpful'),
       COUNT(*) FILTER (WHERE feedback.outcome = 'resolved'),
       COALESCE(SUM(stats.tool_calls), 0),
       COALESCE(SUM(stats.duration_ms), 0)
FROM ai_turn_runs AS turn
JOIN ai_sessions AS session ON session.id = turn.session_id
LEFT JOIN ai_turn_feedback AS feedback
  ON feedback.session_id = turn.session_id AND feedback.turn = turn.turn
LEFT JOIN LATERAL (
    SELECT COUNT(*) FILTER (WHERE event.kind = 'tool_call') AS tool_calls,
           COALESCE(SUM(event.duration_ms), 0) AS duration_ms
    FROM ai_session_events AS event
    WHERE event.session_id = turn.session_id AND event.turn = turn.turn
) AS stats ON TRUE
WHERE session.initiator_user_id = $1::uuid
  AND session.tenant_id = $2::uuid
  AND session.project_id = $3::uuid
  AND session.cluster_id = $4::uuid
  AND turn.started_at >= $5 AND turn.started_at < $6
  AND turn.status <> 'running'`, query.InitiatorUserID, query.TenantID,
		query.ProjectID, query.ClusterID, query.From, query.To,
	).Scan(&result.Turns, &result.Succeeded, &result.Failed, &result.Canceled,
		&result.Rated, &result.Helpful, &result.Resolved, &result.ToolCalls, &durationMilliseconds)
	if err != nil {
		return AIEvaluation{}, fmt.Errorf("evaluate AIOps turns: %w", err)
	}
	result.Duration = time.Duration(durationMilliseconds) * time.Millisecond

	failureRows, err := store.pool.Query(ctx, `
SELECT turn.failure, COUNT(*)
FROM ai_turn_runs AS turn
JOIN ai_sessions AS session ON session.id = turn.session_id
WHERE session.initiator_user_id = $1::uuid
  AND session.tenant_id = $2::uuid AND session.project_id = $3::uuid
  AND session.cluster_id = $4::uuid
  AND turn.started_at >= $5 AND turn.started_at < $6
  AND turn.failure <> ''
GROUP BY turn.failure`, query.InitiatorUserID, query.TenantID, query.ProjectID,
		query.ClusterID, query.From, query.To)
	if err != nil {
		return AIEvaluation{}, fmt.Errorf("read AIOps failure evaluation: %w", err)
	}
	defer failureRows.Close()
	for failureRows.Next() {
		var name string
		var count int64
		if err := failureRows.Scan(&name, &count); err != nil {
			return AIEvaluation{}, fmt.Errorf("scan AIOps failure evaluation: %w", err)
		}
		result.FailureCounts[name] = count
	}
	if err := failureRows.Err(); err != nil {
		return AIEvaluation{}, fmt.Errorf("iterate AIOps failure evaluation: %w", err)
	}

	reasonRows, err := store.pool.Query(ctx, `
SELECT reason, COUNT(*)
FROM ai_turn_feedback AS feedback
JOIN ai_sessions AS session ON session.id = feedback.session_id
JOIN ai_turn_runs AS turn ON turn.session_id = feedback.session_id AND turn.turn = feedback.turn
CROSS JOIN LATERAL unnest(feedback.reasons) AS reason
WHERE session.initiator_user_id = $1::uuid
  AND session.tenant_id = $2::uuid AND session.project_id = $3::uuid
  AND session.cluster_id = $4::uuid
  AND turn.started_at >= $5 AND turn.started_at < $6
GROUP BY reason`, query.InitiatorUserID, query.TenantID, query.ProjectID,
		query.ClusterID, query.From, query.To)
	if err != nil {
		return AIEvaluation{}, fmt.Errorf("read AIOps feedback reasons: %w", err)
	}
	defer reasonRows.Close()
	for reasonRows.Next() {
		var name string
		var count int64
		if err := reasonRows.Scan(&name, &count); err != nil {
			return AIEvaluation{}, fmt.Errorf("scan AIOps feedback reason: %w", err)
		}
		result.ReasonCounts[name] = count
	}
	if err := reasonRows.Err(); err != nil {
		return AIEvaluation{}, fmt.Errorf("iterate AIOps feedback reasons: %w", err)
	}
	return result, nil
}

// ListAISessionEventsForInitiator reads a trail in order, starting after a
// sequence the caller already has.
//
// `afterSequence` is what makes a reconnect cheap and exact: a client that saw
// up to N asks for what came after N and gets neither a duplicate nor a gap.
// Zero reads the trail from its beginning.
func (store *AISessionStore) ListAISessionEventsForInitiator(
	ctx context.Context,
	sessionID string,
	initiatorUserID string,
	afterSequence int32,
	retentionCutoff time.Time,
	limit int,
) ([]AISessionEvent, error) {
	rows, err := store.pool.Query(ctx, `
SELECT event.sequence, event.turn, event.kind, event.content, event.truncated,
       event.occurred_at, event.duration_ms
FROM ai_session_events AS event
JOIN ai_sessions AS session ON session.id = event.session_id
WHERE event.session_id = $1::uuid
  AND session.initiator_user_id = $2::uuid
  AND session.last_activity_at > $3
  AND event.sequence > $4
ORDER BY event.sequence
LIMIT $5`, sessionID, initiatorUserID, retentionCutoff, afterSequence, limit)
	if err != nil {
		return nil, fmt.Errorf("list AIOps session events: %w", err)
	}
	defer rows.Close()
	events := make([]AISessionEvent, 0)
	for rows.Next() {
		var event AISessionEvent
		var durationMilliseconds int64
		if err := rows.Scan(
			&event.Sequence,
			&event.Turn,
			&event.Kind,
			&event.Content,
			&event.Truncated,
			&event.OccurredAt,
			&durationMilliseconds,
		); err != nil {
			return nil, fmt.Errorf("scan AIOps session event: %w", err)
		}
		event.Duration = time.Duration(durationMilliseconds) * time.Millisecond
		events = append(events, event)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate AIOps session events: %w", err)
	}
	return events, nil
}

// InterruptAITurns ends every turn still marked running and writes one error
// entry into each trail, reporting how many it ended.
//
// A turn is driven by a goroutine in one Server process, so a session left
// working after that process is gone describes something that is not
// happening. Called at startup. The entry is appended in the same statement as
// the state change, so a trail never ends without saying why.
func (store *AISessionStore) InterruptAITurns(
	ctx context.Context,
	input InterruptAITurnsParams,
) (int64, error) {
	command, err := store.pool.Exec(ctx, `
WITH interrupted AS (
    UPDATE ai_sessions
    SET status = 'idle',
        last_turn_status = 'failed',
        last_turn_failure = $1,
        next_sequence = next_sequence + 1,
        last_activity_at = $2
    WHERE status = 'working'
    RETURNING id, current_turn AS turn, next_sequence - 1 AS sequence
), finished AS (
    UPDATE ai_turn_runs AS turn
    SET status = 'failed', failure = $1, finished_at = $2
    FROM interrupted
    WHERE turn.session_id = interrupted.id AND turn.turn = interrupted.turn
      AND turn.status = 'running'
    RETURNING turn.session_id, turn.turn
)
INSERT INTO ai_session_events (
    session_id, sequence, turn, kind, content, occurred_at
)
SELECT interrupted.id, interrupted.sequence, interrupted.turn, 'error', $3::jsonb, $2
FROM interrupted
JOIN finished ON finished.session_id = interrupted.id AND finished.turn = interrupted.turn`,
		input.Failure,
		input.Now,
		input.Content,
	)
	if err != nil {
		return 0, fmt.Errorf("interrupt AIOps turns: %w", err)
	}
	return command.RowsAffected(), nil
}

// classifyMissingSession separates "this session is in the wrong state" from
// "there is no such session", which the state-guarded statements cannot tell
// apart on their own: both match no row.
func (store *AISessionStore) classifyMissingSession(
	ctx context.Context,
	sessionID string,
	stateError error,
) error {
	var exists bool
	if err := store.pool.QueryRow(ctx,
		"SELECT EXISTS (SELECT 1 FROM ai_sessions WHERE id = $1::uuid)", sessionID,
	).Scan(&exists); err != nil {
		return fmt.Errorf("check AIOps session state: %w", err)
	}
	if !exists {
		return ErrAISessionNotFound
	}
	return stateError
}

// deleteAgedOutAISessions reclaims whole sessions, entries included through the
// foreign key. Detached from the caller's context so a cancelled request does
// not leave the reclamation half done.
func (store *AISessionStore) deleteAgedOutAISessions(
	ctx context.Context,
	cutoff time.Time,
) error {
	cleanupContext, cancel := context.WithTimeout(context.WithoutCancel(ctx), 2*time.Second)
	defer cancel()
	_, err := store.pool.Exec(cleanupContext,
		"DELETE FROM ai_sessions WHERE last_activity_at <= $1", cutoff)
	return err
}
