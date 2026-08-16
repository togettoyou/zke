package store

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

const endpointProfileColumns = `
    id::text,
    name,
    registration_url,
    quic_address,
    registration_ca_certificate_pem,
    enabled,
    revision,
    COALESCE(created_by_user_id::text, ''),
    COALESCE(updated_by_user_id::text, ''),
    created_at,
    updated_at`

func scanEndpointProfile(row pgx.Row) (AgentEndpointProfile, error) {
	var profile AgentEndpointProfile
	err := row.Scan(
		&profile.ID,
		&profile.Name,
		&profile.RegistrationURL,
		&profile.QUICAddress,
		&profile.RegistrationCACertificatePEM,
		&profile.Enabled,
		&profile.Revision,
		&profile.CreatedByUserID,
		&profile.UpdatedByUserID,
		&profile.CreatedAt,
		&profile.UpdatedAt,
	)
	return profile, err
}

func (store *PlatformSettingsStore) ListEndpointProfiles(
	ctx context.Context,
) ([]AgentEndpointProfile, error) {
	rows, err := store.pool.Query(ctx, `SELECT `+endpointProfileColumns+`
FROM agent_endpoint_profiles
ORDER BY lower(name), id`)
	if err != nil {
		return nil, fmt.Errorf("list Agent endpoint profiles: %w", err)
	}
	defer rows.Close()
	profiles := make([]AgentEndpointProfile, 0)
	for rows.Next() {
		profile, err := scanEndpointProfile(rows)
		if err != nil {
			return nil, fmt.Errorf("scan Agent endpoint profile: %w", err)
		}
		profiles = append(profiles, profile)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate Agent endpoint profiles: %w", err)
	}
	return profiles, nil
}

func (store *PlatformSettingsStore) GetEndpointProfile(
	ctx context.Context,
	id string,
) (AgentEndpointProfile, error) {
	profile, err := scanEndpointProfile(store.pool.QueryRow(ctx, `SELECT `+endpointProfileColumns+`
FROM agent_endpoint_profiles
WHERE id = $1`, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return AgentEndpointProfile{}, ErrEndpointProfileNotFound
	}
	if err != nil {
		return AgentEndpointProfile{}, fmt.Errorf("get Agent endpoint profile: %w", err)
	}
	return profile, nil
}

func (store *PlatformSettingsStore) CreateEndpointProfile(
	ctx context.Context,
	input CreateAgentEndpointProfileParams,
) (AgentEndpointProfile, error) {
	profile, err := scanEndpointProfile(store.pool.QueryRow(ctx, `
INSERT INTO agent_endpoint_profiles (
    id, name, registration_url, quic_address,
    registration_ca_certificate_pem, enabled, created_by_user_id,
    updated_by_user_id, created_at, updated_at
) VALUES ($1, $2, $3, $4, $5, $6, $7, $7, $8, $8)
RETURNING `+endpointProfileColumns,
		input.ID,
		input.Name,
		input.RegistrationURL,
		input.QUICAddress,
		input.RegistrationCACertificatePEM,
		input.Enabled,
		input.ActorUserID,
		input.Now,
	))
	if isUniqueViolation(err) {
		return AgentEndpointProfile{}, ErrEndpointProfileConflict
	}
	if err != nil {
		return AgentEndpointProfile{}, fmt.Errorf("create Agent endpoint profile: %w", err)
	}
	return profile, nil
}

func (store *PlatformSettingsStore) UpdateEndpointProfile(
	ctx context.Context,
	input UpdateAgentEndpointProfileParams,
) (AgentEndpointProfile, error) {
	profile, err := scanEndpointProfile(store.pool.QueryRow(ctx, `
UPDATE agent_endpoint_profiles
SET name = $2,
    registration_url = $3,
    quic_address = $4,
    registration_ca_certificate_pem = $5,
    enabled = $6,
    revision = revision + 1,
    updated_by_user_id = $7,
    updated_at = $8
WHERE id = $1 AND revision = $9
RETURNING `+endpointProfileColumns,
		input.ID,
		input.Name,
		input.RegistrationURL,
		input.QUICAddress,
		input.RegistrationCACertificatePEM,
		input.Enabled,
		input.ActorUserID,
		input.Now,
		input.ExpectedRevision,
	))
	if isUniqueViolation(err) {
		return AgentEndpointProfile{}, ErrEndpointProfileConflict
	}
	if errors.Is(err, pgx.ErrNoRows) {
		var exists bool
		if lookupErr := store.pool.QueryRow(ctx,
			"SELECT EXISTS (SELECT 1 FROM agent_endpoint_profiles WHERE id = $1)",
			input.ID,
		).Scan(&exists); lookupErr != nil {
			return AgentEndpointProfile{}, fmt.Errorf("check Agent endpoint profile update: %w", lookupErr)
		}
		if !exists {
			return AgentEndpointProfile{}, ErrEndpointProfileNotFound
		}
		return AgentEndpointProfile{}, ErrEndpointProfileConflict
	}
	if err != nil {
		return AgentEndpointProfile{}, fmt.Errorf("update Agent endpoint profile: %w", err)
	}
	return profile, nil
}

func (store *PlatformSettingsStore) DeleteEndpointProfile(
	ctx context.Context,
	id string,
) error {
	command, err := store.pool.Exec(ctx, `
DELETE FROM agent_endpoint_profiles AS profile
WHERE profile.id = $1
  AND NOT EXISTS (
      SELECT 1 FROM platform_settings
      WHERE default_endpoint_profile_id = profile.id
  )`, id)
	if err != nil {
		return fmt.Errorf("delete Agent endpoint profile: %w", err)
	}
	if command.RowsAffected() == 1 {
		return nil
	}
	var exists bool
	if err := store.pool.QueryRow(ctx,
		"SELECT EXISTS (SELECT 1 FROM agent_endpoint_profiles WHERE id = $1)", id,
	).Scan(&exists); err != nil {
		return fmt.Errorf("check Agent endpoint profile deletion: %w", err)
	}
	if !exists {
		return ErrEndpointProfileNotFound
	}
	return ErrEndpointProfileInUse
}

// platformSettingsColumns is shared by the read and the update so the two can
// never drift into scanning different shapes into the same struct.
const platformSettingsColumns = `
    default_endpoint_profile_id::text,
    agent_image,
    agent_image_pull_policy,
    cluster_terminal_image,
    cluster_terminal_image_pull_policy,
    metrics_collector_image,
    metrics_collector_image_pull_policy,
    metrics_collector_cpu_request,
    metrics_collector_memory_request,
    metrics_collector_cpu_limit,
    metrics_collector_memory_limit,
    kube_state_metrics_image,
    kube_state_metrics_image_pull_policy,
    kube_state_metrics_cpu_request,
    kube_state_metrics_memory_request,
    kube_state_metrics_cpu_limit,
    kube_state_metrics_memory_limit,
    node_exporter_image,
    node_exporter_image_pull_policy,
    node_exporter_cpu_request,
    node_exporter_memory_request,
    node_exporter_cpu_limit,
    node_exporter_memory_limit,
    cluster_terminal_session_ttl_seconds,
    revision,
    COALESCE(updated_by_user_id::text, ''),
    updated_at`

func scanPlatformSettings(row pgx.Row) (PlatformSettings, error) {
	var settings PlatformSettings
	var terminalSessionTTLSeconds int32
	err := row.Scan(
		&settings.DefaultEndpointProfileID,
		&settings.AgentImage,
		&settings.AgentImagePullPolicy,
		&settings.ClusterTerminalImage,
		&settings.ClusterTerminalImagePullPolicy,
		&settings.MetricsCollectorImage,
		&settings.MetricsCollectorImagePullPolicy,
		&settings.MetricsCollectorCPURequest,
		&settings.MetricsCollectorMemoryRequest,
		&settings.MetricsCollectorCPULimit,
		&settings.MetricsCollectorMemoryLimit,
		&settings.KubeStateMetricsImage,
		&settings.KubeStateMetricsImagePullPolicy,
		&settings.KubeStateMetricsCPURequest,
		&settings.KubeStateMetricsMemoryRequest,
		&settings.KubeStateMetricsCPULimit,
		&settings.KubeStateMetricsMemoryLimit,
		&settings.NodeExporterImage,
		&settings.NodeExporterImagePullPolicy,
		&settings.NodeExporterCPURequest,
		&settings.NodeExporterMemoryRequest,
		&settings.NodeExporterCPULimit,
		&settings.NodeExporterMemoryLimit,
		&terminalSessionTTLSeconds,
		&settings.Revision,
		&settings.UpdatedByUserID,
		&settings.UpdatedAt,
	)
	if err != nil {
		return PlatformSettings{}, err
	}
	settings.ClusterTerminalSessionTTL = time.Duration(terminalSessionTTLSeconds) * time.Second
	return settings, nil
}

func (store *PlatformSettingsStore) GetSettings(
	ctx context.Context,
) (PlatformSettings, error) {
	settings, err := scanPlatformSettings(store.pool.QueryRow(ctx, `SELECT `+
		platformSettingsColumns+`
FROM platform_settings
WHERE singleton = true`))
	if err != nil {
		return PlatformSettings{}, fmt.Errorf("get platform settings: %w", err)
	}
	return settings, nil
}

func (store *PlatformSettingsStore) ReconcileDefaultEndpoint(
	ctx context.Context,
	input ReconcileDefaultEndpointParams,
) (AgentEndpointProfile, error) {
	transaction, err := store.pool.Begin(ctx)
	if err != nil {
		return AgentEndpointProfile{}, fmt.Errorf("begin platform default endpoint reconciliation: %w", err)
	}
	defer func() { _ = transaction.Rollback(ctx) }()

	var profileID string
	err = transaction.QueryRow(ctx, `
SELECT id::text
FROM agent_endpoint_profiles
WHERE id::text = ANY($3::text[])
  AND registration_url = $1
  AND quic_address = $2
ORDER BY id
LIMIT 1`, input.RegistrationURL, input.QUICAddress, input.PresetProfileIDs).Scan(&profileID)
	if errors.Is(err, pgx.ErrNoRows) {
		profileID = input.ReservedID
		_, err = transaction.Exec(ctx, `
INSERT INTO agent_endpoint_profiles (
    id, name, registration_url, quic_address,
    registration_ca_certificate_pem, enabled, created_at, updated_at
) VALUES ($1, $2, $3, $4, '', true, $5, $5)
ON CONFLICT (id) DO UPDATE
SET name = EXCLUDED.name,
    registration_url = EXCLUDED.registration_url,
    quic_address = EXCLUDED.quic_address,
    registration_ca_certificate_pem = '',
    enabled = true,
    revision = agent_endpoint_profiles.revision + 1,
    updated_by_user_id = NULL,
    updated_at = EXCLUDED.updated_at
WHERE agent_endpoint_profiles.name IS DISTINCT FROM EXCLUDED.name
   OR agent_endpoint_profiles.registration_url IS DISTINCT FROM EXCLUDED.registration_url
   OR agent_endpoint_profiles.quic_address IS DISTINCT FROM EXCLUDED.quic_address
   OR agent_endpoint_profiles.registration_ca_certificate_pem IS DISTINCT FROM ''
   OR agent_endpoint_profiles.enabled = false`,
			profileID, input.ReservedName, input.RegistrationURL, input.QUICAddress, input.Now)
	}
	if err != nil {
		return AgentEndpointProfile{}, fmt.Errorf("reconcile platform default endpoint profile: %w", err)
	}
	if _, err := transaction.Exec(ctx, `
UPDATE agent_endpoint_profiles
SET enabled = true,
    revision = revision + 1,
    updated_by_user_id = NULL,
    updated_at = $2
WHERE id = $1 AND enabled = false`, profileID, input.Now); err != nil {
		return AgentEndpointProfile{}, fmt.Errorf("enable platform default endpoint profile: %w", err)
	}
	if _, err := transaction.Exec(ctx, `
UPDATE platform_settings
SET default_endpoint_profile_id = $1,
    revision = revision + 1,
    updated_by_user_id = NULL,
    updated_at = $2
WHERE singleton = true AND default_endpoint_profile_id <> $1`, profileID, input.Now); err != nil {
		return AgentEndpointProfile{}, fmt.Errorf("select platform default endpoint profile: %w", err)
	}
	if profileID != input.ReservedID {
		if _, err := transaction.Exec(ctx, `
DELETE FROM agent_endpoint_profiles
WHERE id = $1`, input.ReservedID); err != nil {
			return AgentEndpointProfile{}, fmt.Errorf("remove superseded deployment endpoint profile: %w", err)
		}
	}
	profile, err := scanEndpointProfile(transaction.QueryRow(ctx, `SELECT `+endpointProfileColumns+`
FROM agent_endpoint_profiles
WHERE id = $1`, profileID))
	if err != nil {
		return AgentEndpointProfile{}, fmt.Errorf("read reconciled platform default endpoint profile: %w", err)
	}
	if err := transaction.Commit(ctx); err != nil {
		return AgentEndpointProfile{}, fmt.Errorf("commit platform default endpoint reconciliation: %w", err)
	}
	return profile, nil
}

func (store *PlatformSettingsStore) UpdateSettings(
	ctx context.Context,
	input UpdatePlatformSettingsParams,
) (PlatformSettings, error) {
	settings, err := scanPlatformSettings(store.pool.QueryRow(ctx, `
UPDATE platform_settings
SET agent_image = $1,
    agent_image_pull_policy = $2,
    cluster_terminal_image = $3,
    cluster_terminal_image_pull_policy = $4,
    metrics_collector_image = $5,
    metrics_collector_image_pull_policy = $6,
    metrics_collector_cpu_request = $7,
    metrics_collector_memory_request = $8,
    metrics_collector_cpu_limit = $9,
    metrics_collector_memory_limit = $10,
    kube_state_metrics_image = $11,
    kube_state_metrics_image_pull_policy = $12,
    kube_state_metrics_cpu_request = $13,
    kube_state_metrics_memory_request = $14,
    kube_state_metrics_cpu_limit = $15,
    kube_state_metrics_memory_limit = $16,
    node_exporter_image = $17,
    node_exporter_image_pull_policy = $18,
    node_exporter_cpu_request = $19,
    node_exporter_memory_request = $20,
    node_exporter_cpu_limit = $21,
    node_exporter_memory_limit = $22,
    cluster_terminal_session_ttl_seconds = $23,
    revision = revision + 1,
    updated_by_user_id = $24,
    updated_at = $25
WHERE singleton = true
  AND revision = $26
RETURNING`+platformSettingsColumns,
		input.AgentImage,
		input.AgentImagePullPolicy,
		input.ClusterTerminalImage,
		input.ClusterTerminalImagePullPolicy,
		input.MetricsCollectorImage,
		input.MetricsCollectorImagePullPolicy,
		input.MetricsCollectorCPURequest,
		input.MetricsCollectorMemoryRequest,
		input.MetricsCollectorCPULimit,
		input.MetricsCollectorMemoryLimit,
		input.KubeStateMetricsImage,
		input.KubeStateMetricsImagePullPolicy,
		input.KubeStateMetricsCPURequest,
		input.KubeStateMetricsMemoryRequest,
		input.KubeStateMetricsCPULimit,
		input.KubeStateMetricsMemoryLimit,
		input.NodeExporterImage,
		input.NodeExporterImagePullPolicy,
		input.NodeExporterCPURequest,
		input.NodeExporterMemoryRequest,
		input.NodeExporterCPULimit,
		input.NodeExporterMemoryLimit,
		int32(input.ClusterTerminalSessionTTL/time.Second),
		input.ActorUserID,
		input.Now,
		input.ExpectedRevision,
	))
	if errors.Is(err, pgx.ErrNoRows) {
		return PlatformSettings{}, ErrPlatformSettingsConflict
	}
	if err != nil {
		return PlatformSettings{}, fmt.Errorf("update platform settings: %w", err)
	}
	return settings, nil
}

func isUniqueViolation(err error) bool {
	var postgresError *pgconn.PgError
	return errors.As(err, &postgresError) && postgresError.Code == "23505"
}
