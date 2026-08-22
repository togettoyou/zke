package store

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// ErrAIModelSettingsConflict reports a save that lost a revision race. Its own
// error rather than the platform settings one because the two sections carry
// independent revisions: reporting a conflict on the wrong section would tell
// an operator to reload a form nobody changed.
var (
	ErrAIModelSettingsConflict      = errors.New("AI model settings revision conflict")
	ErrAIModelSettingsNotConfigured = errors.New("AI model endpoint is not configured")
)

// AIModelSettings is the stored row, API Key included. It is the only shape
// that carries the credential; everything above this package works with a
// projection that reports whether a key is set instead of what it is.
type AIModelSettings struct {
	Enabled             bool
	BaseURL             string
	Model               string
	APIProtocol         string
	APIKey              string
	ContextWindowTokens int32
	MaxOutputTokens     int32
	RequestTimeout      time.Duration
	Revision            int64

	UpdatedByUserID string
	UpdatedAt       time.Time
}

// UpdateAIModelSettingsParams is a whole-section write: this section is one
// form, saved by one button, and the endpoint, the model name and the key are
// only meaningful together.
//
// APIKey is the exception, and it is a three-state field: nil keeps the stored
// key, which is what a save that did not touch the field must do given the
// value is never sent back to the browser to be echoed; a pointer to an empty
// string clears it, which is how an operator moves to an endpoint that takes no
// credential; anything else replaces it.
type UpdateAIModelSettingsParams struct {
	BaseURL             string
	Model               string
	APIProtocol         string
	APIKey              *string
	ContextWindowTokens int32
	MaxOutputTokens     int32
	RequestTimeout      time.Duration
	ExpectedRevision    int64
	ActorUserID         string
	Now                 time.Time
}

type SetAIModelEnabledParams struct {
	Enabled          bool
	ExpectedRevision int64
	ActorUserID      string
	Now              time.Time
}

type AIModelSettingsStore struct {
	pool *pgxpool.Pool
}

func NewAIModelSettingsStore(pool *pgxpool.Pool) *AIModelSettingsStore {
	return &AIModelSettingsStore{pool: pool}
}

const aiModelSettingsColumns = `
    enabled,
    base_url,
    model,
    api_protocol,
    api_key,
    context_window_tokens,
    max_output_tokens,
    request_timeout_seconds,
    revision,
    COALESCE(updated_by_user_id::text, ''),
    updated_at`

func scanAIModelSettings(row pgx.Row) (AIModelSettings, error) {
	var settings AIModelSettings
	var requestTimeoutSeconds int32
	err := row.Scan(
		&settings.Enabled,
		&settings.BaseURL,
		&settings.Model,
		&settings.APIProtocol,
		&settings.APIKey,
		&settings.ContextWindowTokens,
		&settings.MaxOutputTokens,
		&requestTimeoutSeconds,
		&settings.Revision,
		&settings.UpdatedByUserID,
		&settings.UpdatedAt,
	)
	if err != nil {
		return AIModelSettings{}, err
	}
	settings.RequestTimeout = time.Duration(requestTimeoutSeconds) * time.Second
	return settings, nil
}

func (store *AIModelSettingsStore) GetAIModelSettings(
	ctx context.Context,
) (AIModelSettings, error) {
	settings, err := scanAIModelSettings(store.pool.QueryRow(ctx, `SELECT `+
		aiModelSettingsColumns+`
FROM ai_model_settings
WHERE singleton = true`))
	if err != nil {
		return AIModelSettings{}, fmt.Errorf("get AI model settings: %w", err)
	}
	return settings, nil
}

// UpdateAIModelSettings writes the section under a revision check and returns
// the stored row, so the caller reports what was written rather than what it
// asked for.
func (store *AIModelSettingsStore) UpdateAIModelSettings(
	ctx context.Context,
	input UpdateAIModelSettingsParams,
) (AIModelSettings, error) {
	settings, err := scanAIModelSettings(store.pool.QueryRow(ctx, `
UPDATE ai_model_settings
SET base_url = $1,
    model = $2,
    api_protocol = $3,
    api_key = COALESCE($4, api_key),
    context_window_tokens = $5,
    max_output_tokens = $6,
    request_timeout_seconds = $7,
    revision = revision + 1,
    updated_by_user_id = $8,
    updated_at = $9
WHERE singleton = true
  AND revision = $10
  AND (NOT enabled OR ($1 <> '' AND $2 <> ''))
RETURNING `+aiModelSettingsColumns,
		input.BaseURL,
		input.Model,
		input.APIProtocol,
		input.APIKey,
		input.ContextWindowTokens,
		input.MaxOutputTokens,
		int32(input.RequestTimeout/time.Second),
		input.ActorUserID,
		input.Now,
		input.ExpectedRevision,
	))
	if err != nil {
		// The row is seeded by the migration and never deleted, so no rows
		// returned means the revision moved. There is no "not found" case to
		// separate out.
		if errors.Is(err, pgx.ErrNoRows) {
			return AIModelSettings{}, store.classifyAIModelSettingsWrite(ctx, input.ExpectedRevision)
		}
		return AIModelSettings{}, fmt.Errorf("update AI model settings: %w", err)
	}
	return settings, nil
}

// SetAIModelEnabled changes only the runtime gate. Configuration drafts and
// their save button never carry this state, so toggling takes effect with one
// dedicated request and one revision check.
func (store *AIModelSettingsStore) SetAIModelEnabled(
	ctx context.Context, input SetAIModelEnabledParams,
) (AIModelSettings, error) {
	settings, err := scanAIModelSettings(store.pool.QueryRow(ctx, `
UPDATE ai_model_settings
SET enabled = $1,
    revision = revision + 1,
    updated_by_user_id = $2,
    updated_at = $3
WHERE singleton = true AND revision = $4
  AND (NOT $1 OR (base_url <> '' AND model <> ''))
RETURNING `+aiModelSettingsColumns,
		input.Enabled, input.ActorUserID, input.Now, input.ExpectedRevision,
	))
	if errors.Is(err, pgx.ErrNoRows) {
		return AIModelSettings{}, store.classifyAIModelSettingsWrite(ctx, input.ExpectedRevision)
	}
	if err != nil {
		return AIModelSettings{}, fmt.Errorf("set AI model enabled: %w", err)
	}
	return settings, nil
}

func (store *AIModelSettingsStore) classifyAIModelSettingsWrite(
	ctx context.Context, expectedRevision int64,
) error {
	var revision int64
	if err := store.pool.QueryRow(ctx, `
SELECT revision FROM ai_model_settings WHERE singleton = true`).Scan(&revision); err != nil {
		return fmt.Errorf("classify AI model settings write: %w", err)
	}
	if revision != expectedRevision {
		return ErrAIModelSettingsConflict
	}
	return ErrAIModelSettingsNotConfigured
}
