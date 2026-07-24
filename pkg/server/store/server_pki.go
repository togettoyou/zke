package store

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

const serverPKIAdvisoryLockID int64 = 0x5a4b45504b49

type ServerPKIState struct {
	AgentClientCAFingerprint            string
	AgentClientCAExpiresAt              time.Time
	AgentListenerCAFingerprint          string
	AgentListenerCAExpiresAt            time.Time
	AgentListenerCertificateFingerprint string
	AgentListenerCertificateExpiresAt   time.Time
}

type ServerPKILock struct {
	connection *pgxpool.Conn
}

func AcquireServerPKILock(
	ctx context.Context,
	pool *pgxpool.Pool,
) (*ServerPKILock, error) {
	connection, err := pool.Acquire(ctx)
	if err != nil {
		return nil, fmt.Errorf("acquire PostgreSQL connection for Server PKI: %w", err)
	}
	if _, err := connection.Exec(
		ctx,
		"SELECT pg_advisory_lock($1)",
		serverPKIAdvisoryLockID,
	); err != nil {
		connection.Release()
		return nil, fmt.Errorf("lock Server PKI initialization: %w", err)
	}
	return &ServerPKILock{connection: connection}, nil
}

func (lock *ServerPKILock) Close(ctx context.Context) error {
	if lock == nil || lock.connection == nil {
		return nil
	}
	connection := lock.connection
	lock.connection = nil
	defer connection.Release()
	if _, err := connection.Exec(
		ctx,
		"SELECT pg_advisory_unlock($1)",
		serverPKIAdvisoryLockID,
	); err != nil {
		return fmt.Errorf("unlock Server PKI initialization: %w", err)
	}
	return nil
}

func (lock *ServerPKILock) Load(
	ctx context.Context,
) (ServerPKIState, bool, error) {
	var state ServerPKIState
	err := lock.connection.QueryRow(ctx, `
SELECT
    agent_client_ca_fingerprint,
    agent_client_ca_expires_at,
    agent_listener_ca_fingerprint,
    agent_listener_ca_expires_at,
    agent_listener_certificate_fingerprint,
    agent_listener_certificate_expires_at
FROM server_pki_state
WHERE singleton = true
`).Scan(
		&state.AgentClientCAFingerprint,
		&state.AgentClientCAExpiresAt,
		&state.AgentListenerCAFingerprint,
		&state.AgentListenerCAExpiresAt,
		&state.AgentListenerCertificateFingerprint,
		&state.AgentListenerCertificateExpiresAt,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return ServerPKIState{}, false, nil
	}
	if err != nil {
		return ServerPKIState{}, false, fmt.Errorf("load Server PKI state: %w", err)
	}
	return state, true, nil
}

func (lock *ServerPKILock) HasAgentSecurityState(
	ctx context.Context,
) (bool, error) {
	var exists bool
	err := lock.connection.QueryRow(ctx, `
SELECT EXISTS (SELECT 1 FROM agents)
    OR EXISTS (SELECT 1 FROM agent_credentials)
    OR EXISTS (SELECT 1 FROM enrollments WHERE consumed_at IS NOT NULL)
`).Scan(&exists)
	if err != nil {
		return false, fmt.Errorf("check existing Agent security state: %w", err)
	}
	return exists, nil
}

func (lock *ServerPKILock) Save(
	ctx context.Context,
	state ServerPKIState,
) error {
	_, err := lock.connection.Exec(ctx, `
INSERT INTO server_pki_state (
    singleton,
    agent_client_ca_fingerprint,
    agent_client_ca_expires_at,
    agent_listener_ca_fingerprint,
    agent_listener_ca_expires_at,
    agent_listener_certificate_fingerprint,
    agent_listener_certificate_expires_at
)
VALUES (true, $1, $2, $3, $4, $5, $6)
ON CONFLICT (singleton) DO UPDATE
SET agent_client_ca_fingerprint = EXCLUDED.agent_client_ca_fingerprint,
    agent_client_ca_expires_at = EXCLUDED.agent_client_ca_expires_at,
    agent_listener_ca_fingerprint = EXCLUDED.agent_listener_ca_fingerprint,
    agent_listener_ca_expires_at = EXCLUDED.agent_listener_ca_expires_at,
    agent_listener_certificate_fingerprint =
        EXCLUDED.agent_listener_certificate_fingerprint,
    agent_listener_certificate_expires_at =
        EXCLUDED.agent_listener_certificate_expires_at,
    updated_at = now()
`,
		state.AgentClientCAFingerprint,
		state.AgentClientCAExpiresAt,
		state.AgentListenerCAFingerprint,
		state.AgentListenerCAExpiresAt,
		state.AgentListenerCertificateFingerprint,
		state.AgentListenerCertificateExpiresAt,
	)
	if err != nil {
		return fmt.Errorf("save Server PKI state: %w", err)
	}
	return nil
}
