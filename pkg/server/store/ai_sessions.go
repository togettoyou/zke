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
	SessionID  string
	Content    []byte
	Truncated  bool
	OccurredAt time.Time
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
	var event AISessionEvent
	err := store.pool.QueryRow(ctx, `
WITH opened AS (
    UPDATE ai_sessions
    SET status = 'working',
        current_turn = current_turn + 1,
        next_sequence = next_sequence + 1,
        last_turn_status = '',
        last_turn_failure = '',
        last_activity_at = $4
    WHERE id = $1::uuid AND status = 'idle' AND archived_at IS NULL
    RETURNING current_turn AS turn, next_sequence - 1 AS sequence
)
INSERT INTO ai_session_events (
    session_id, sequence, turn, kind, content, truncated, occurred_at
)
SELECT $1::uuid, opened.sequence, opened.turn, 'input', $2::jsonb, $3, $4
FROM opened
RETURNING sequence, turn`,
		input.SessionID,
		input.Content,
		input.Truncated,
		input.OccurredAt,
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
UPDATE ai_sessions
SET status = 'idle',
    last_turn_status = $2,
    last_turn_failure = $3,
    last_activity_at = $4
WHERE id = $1::uuid AND status = 'working'
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
)
INSERT INTO ai_session_events (
    session_id, sequence, turn, kind, content, occurred_at
)
SELECT interrupted.id, interrupted.sequence, interrupted.turn, 'error', $3::jsonb, $2
FROM interrupted`,
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
