package store

import (
	"context"
	"errors"
	"fmt"
	"strings"
)

func (store *AuthStore) RecordLoginAudit(
	ctx context.Context,
	targetUserID *string,
	result string,
	requestID string,
) error {
	if result != "failed" && result != "denied" {
		return errors.New("login audit result must be failed or denied")
	}
	if strings.TrimSpace(requestID) == "" {
		return errors.New("login audit request ID is required")
	}

	var targetID any
	if targetUserID != nil {
		targetID = *targetUserID
	}
	if _, err := store.pool.Exec(ctx, `
INSERT INTO audit_events (
    id,
    actor_type,
    scope_type,
    action,
    target_type,
    target_id,
    result,
    request_id
)
VALUES (
    gen_random_uuid(),
    'system',
    'global',
    'auth.login',
    'user',
    $1,
    $2,
    $3
)
`, targetID, result, requestID); err != nil {
		return fmt.Errorf("record login audit: %w", err)
	}
	return nil
}
