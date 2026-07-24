CREATE TABLE tenants (
    id uuid PRIMARY KEY,
    name text NOT NULL CHECK (length(btrim(name)) > 0),
    status text NOT NULL CHECK (status IN ('active', 'suspended')),
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE projects (
    id uuid PRIMARY KEY,
    tenant_id uuid NOT NULL REFERENCES tenants (id),
    name text NOT NULL CHECK (length(btrim(name)) > 0),
    status text NOT NULL CHECK (status IN ('active', 'suspended')),
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    UNIQUE (tenant_id, id)
);

CREATE INDEX projects_tenant_id_idx ON projects (tenant_id);

CREATE TABLE users (
    id uuid PRIMARY KEY,
    username_normalized text NOT NULL UNIQUE
        CHECK (username_normalized = lower(btrim(username_normalized)))
        CHECK (length(username_normalized) > 0),
    display_name text NOT NULL CHECK (length(btrim(display_name)) > 0),
    password_hash text NOT NULL CHECK (length(password_hash) > 0),
    status text NOT NULL CHECK (status IN ('active', 'locked', 'disabled')),
    password_changed_at timestamptz NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE user_sessions (
    id uuid PRIMARY KEY,
    user_id uuid NOT NULL REFERENCES users (id),
    token_digest bytea NOT NULL UNIQUE CHECK (octet_length(token_digest) = 32),
    csrf_token_digest bytea NOT NULL CHECK (octet_length(csrf_token_digest) = 32),
    idle_expires_at timestamptz NOT NULL,
    expires_at timestamptz NOT NULL,
    last_seen_at timestamptz NOT NULL DEFAULT now(),
    revoked_at timestamptz,
    created_at timestamptz NOT NULL DEFAULT now(),
    CHECK (idle_expires_at <= expires_at)
);

CREATE INDEX user_sessions_user_id_idx ON user_sessions (user_id);
CREATE INDEX user_sessions_active_expiry_idx
    ON user_sessions (expires_at)
    WHERE revoked_at IS NULL;

CREATE TABLE role_bindings (
    id uuid PRIMARY KEY,
    subject_id uuid NOT NULL REFERENCES users (id),
    role text NOT NULL CHECK (role IN ('admin', 'viewer')),
    scope_type text NOT NULL CHECK (scope_type IN ('global', 'tenant', 'project')),
    tenant_id uuid REFERENCES tenants (id),
    project_id uuid,
    created_at timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT role_bindings_project_scope_fk
        FOREIGN KEY (tenant_id, project_id) REFERENCES projects (tenant_id, id),
    CONSTRAINT role_bindings_scope_shape CHECK (
        (scope_type = 'global' AND tenant_id IS NULL AND project_id IS NULL)
        OR (scope_type = 'tenant' AND tenant_id IS NOT NULL AND project_id IS NULL)
        OR (scope_type = 'project' AND tenant_id IS NOT NULL AND project_id IS NOT NULL)
    ),
    UNIQUE NULLS NOT DISTINCT (subject_id, role, scope_type, tenant_id, project_id)
);

CREATE INDEX role_bindings_subject_id_idx ON role_bindings (subject_id);
CREATE INDEX role_bindings_scope_idx ON role_bindings (scope_type, tenant_id, project_id);

CREATE TABLE clusters (
    id uuid PRIMARY KEY,
    tenant_id uuid NOT NULL REFERENCES tenants (id),
    project_id uuid NOT NULL,
    name text NOT NULL CHECK (length(btrim(name)) > 0),
    status text NOT NULL CHECK (status IN ('pending', 'active', 'revoked')),
    last_seen_at timestamptz,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT clusters_project_scope_fk
        FOREIGN KEY (tenant_id, project_id) REFERENCES projects (tenant_id, id),
    UNIQUE (tenant_id, project_id, id)
);

CREATE INDEX clusters_project_scope_idx ON clusters (tenant_id, project_id);

CREATE TABLE agents (
    id uuid PRIMARY KEY,
    tenant_id uuid NOT NULL REFERENCES tenants (id),
    project_id uuid NOT NULL,
    cluster_id uuid NOT NULL UNIQUE,
    version text NOT NULL CHECK (length(btrim(version)) > 0),
    protocol_version text NOT NULL CHECK (length(btrim(protocol_version)) > 0),
    lifecycle_status text NOT NULL CHECK (lifecycle_status IN ('pending', 'active', 'revoked')),
    health_status text NOT NULL CHECK (health_status IN ('unknown', 'healthy', 'degraded')),
    active_credential_serial text,
    last_seen_at timestamptz,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT agents_cluster_scope_fk
        FOREIGN KEY (tenant_id, project_id, cluster_id)
        REFERENCES clusters (tenant_id, project_id, id),
    UNIQUE (tenant_id, project_id, cluster_id, id)
);

CREATE INDEX agents_project_scope_idx ON agents (tenant_id, project_id);

CREATE TABLE agent_credentials (
    id uuid PRIMARY KEY,
    tenant_id uuid NOT NULL REFERENCES tenants (id),
    project_id uuid NOT NULL,
    cluster_id uuid NOT NULL,
    agent_id uuid NOT NULL,
    serial text NOT NULL UNIQUE CHECK (length(btrim(serial)) > 0),
    csr_fingerprint bytea NOT NULL CHECK (octet_length(csr_fingerprint) > 0),
    certificate_pem text NOT NULL CHECK (length(certificate_pem) > 0),
    expires_at timestamptz NOT NULL,
    revoked_at timestamptz,
    created_at timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT agent_credentials_agent_scope_fk
        FOREIGN KEY (tenant_id, project_id, cluster_id, agent_id)
        REFERENCES agents (tenant_id, project_id, cluster_id, id)
);

CREATE INDEX agent_credentials_agent_id_idx ON agent_credentials (agent_id);
CREATE INDEX agent_credentials_active_expiry_idx
    ON agent_credentials (expires_at)
    WHERE revoked_at IS NULL;

CREATE TABLE server_pki_state (
    singleton boolean PRIMARY KEY DEFAULT true CHECK (singleton),
    agent_client_ca_fingerprint text NOT NULL
        CHECK (length(agent_client_ca_fingerprint) = 64),
    agent_client_ca_expires_at timestamptz NOT NULL,
    agent_listener_ca_fingerprint text NOT NULL
        CHECK (length(agent_listener_ca_fingerprint) = 64),
    agent_listener_ca_expires_at timestamptz NOT NULL,
    agent_listener_certificate_fingerprint text NOT NULL
        CHECK (length(agent_listener_certificate_fingerprint) = 64),
    agent_listener_certificate_expires_at timestamptz NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now()
);

CREATE UNIQUE INDEX agent_credentials_agent_csr_unique
    ON agent_credentials (
        tenant_id,
        project_id,
        cluster_id,
        agent_id,
        csr_fingerprint
    );

CREATE FUNCTION notify_agent_credential_revocation()
RETURNS trigger
LANGUAGE plpgsql
AS $$
BEGIN
    IF OLD.revoked_at IS NULL AND NEW.revoked_at IS NOT NULL THEN
        PERFORM pg_notify(
            'zke_agent_connection_revocations',
            json_build_object(
                'agent_id', NEW.agent_id,
                'certificate_serial', NEW.serial
            )::text
        );
    END IF;
    RETURN NEW;
END;
$$;

CREATE TRIGGER agent_credentials_notify_revocation
AFTER UPDATE OF revoked_at ON agent_credentials
FOR EACH ROW
EXECUTE FUNCTION notify_agent_credential_revocation();

CREATE FUNCTION notify_agent_lifecycle_revocation()
RETURNS trigger
LANGUAGE plpgsql
AS $$
BEGIN
    IF OLD.lifecycle_status <> 'revoked'
       AND NEW.lifecycle_status = 'revoked' THEN
        PERFORM pg_notify(
            'zke_agent_connection_revocations',
            json_build_object('agent_id', NEW.id)::text
        );
    END IF;
    RETURN NEW;
END;
$$;

CREATE TRIGGER agents_notify_revocation
AFTER UPDATE OF lifecycle_status ON agents
FOR EACH ROW
EXECUTE FUNCTION notify_agent_lifecycle_revocation();

CREATE FUNCTION notify_cluster_status_revocation()
RETURNS trigger
LANGUAGE plpgsql
AS $$
BEGIN
    IF OLD.status <> 'revoked' AND NEW.status = 'revoked' THEN
        PERFORM pg_notify(
            'zke_agent_connection_revocations',
            json_build_object('cluster_id', NEW.id)::text
        );
    END IF;
    RETURN NEW;
END;
$$;

CREATE TRIGGER clusters_notify_revocation
AFTER UPDATE OF status ON clusters
FOR EACH ROW
EXECUTE FUNCTION notify_cluster_status_revocation();

CREATE TABLE enrollments (
    id uuid PRIMARY KEY,
    tenant_id uuid NOT NULL REFERENCES tenants (id),
    project_id uuid NOT NULL,
    cluster_name text NOT NULL,
    token_digest bytea NOT NULL UNIQUE CHECK (octet_length(token_digest) > 0),
    created_by_user_id uuid NOT NULL REFERENCES users (id),
    idempotency_key text NOT NULL,
    expires_at timestamptz NOT NULL,
    consumed_at timestamptz,
    revoked_at timestamptz,
    created_at timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT enrollments_project_scope_fk
        FOREIGN KEY (tenant_id, project_id) REFERENCES projects (tenant_id, id),
    CONSTRAINT enrollments_idempotency_key_format CHECK (
        idempotency_key = btrim(idempotency_key)
        AND length(idempotency_key) BETWEEN 16 AND 128
    ),
    CONSTRAINT enrollments_cluster_name_format CHECK (
        cluster_name = btrim(cluster_name)
        AND octet_length(cluster_name) BETWEEN 1 AND 253
    ),
    CONSTRAINT enrollments_creator_idempotency_unique
        UNIQUE (created_by_user_id, project_id, idempotency_key),
    CHECK (consumed_at IS NULL OR revoked_at IS NULL)
);

CREATE INDEX enrollments_project_scope_idx ON enrollments (tenant_id, project_id);
CREATE INDEX enrollments_active_expiry_idx
    ON enrollments (expires_at)
    WHERE consumed_at IS NULL AND revoked_at IS NULL;

CREATE TABLE enrollment_attempts (
    id uuid PRIMARY KEY,
    enrollment_id uuid NOT NULL REFERENCES enrollments (id),
    idempotency_key text NOT NULL CHECK (length(btrim(idempotency_key)) > 0),
    csr_fingerprint bytea NOT NULL CHECK (octet_length(csr_fingerprint) > 0),
    status text NOT NULL CHECK (status IN ('pending', 'succeeded', 'failed')),
    response_json jsonb,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    UNIQUE (enrollment_id, idempotency_key)
);

CREATE INDEX enrollment_attempts_enrollment_id_idx ON enrollment_attempts (enrollment_id);

CREATE TABLE audit_events (
    id uuid PRIMARY KEY,
    actor_type text NOT NULL CHECK (actor_type IN ('user', 'agent', 'system')),
    actor_user_id uuid REFERENCES users (id),
    actor_agent_id uuid REFERENCES agents (id),
    scope_type text NOT NULL CHECK (scope_type IN ('global', 'tenant', 'project', 'cluster')),
    tenant_id uuid REFERENCES tenants (id),
    project_id uuid,
    cluster_id uuid,
    action text NOT NULL CHECK (length(btrim(action)) > 0),
    target_type text NOT NULL CHECK (length(btrim(target_type)) > 0),
    target_id uuid,
    result text NOT NULL CHECK (result IN ('succeeded', 'failed', 'denied')),
    request_id text NOT NULL CHECK (length(btrim(request_id)) > 0),
    created_at timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT audit_events_project_scope_fk
        FOREIGN KEY (tenant_id, project_id) REFERENCES projects (tenant_id, id),
    CONSTRAINT audit_events_cluster_scope_fk
        FOREIGN KEY (tenant_id, project_id, cluster_id)
        REFERENCES clusters (tenant_id, project_id, id),
    CONSTRAINT audit_events_scope_shape CHECK (
        (scope_type = 'global'
            AND tenant_id IS NULL AND project_id IS NULL AND cluster_id IS NULL)
        OR (scope_type = 'tenant'
            AND tenant_id IS NOT NULL AND project_id IS NULL AND cluster_id IS NULL)
        OR (scope_type = 'project'
            AND tenant_id IS NOT NULL AND project_id IS NOT NULL AND cluster_id IS NULL)
        OR (scope_type = 'cluster'
            AND tenant_id IS NOT NULL AND project_id IS NOT NULL AND cluster_id IS NOT NULL)
    ),
    CONSTRAINT audit_events_actor_shape CHECK (
        (actor_type = 'system'
            AND actor_user_id IS NULL AND actor_agent_id IS NULL)
        OR (actor_type = 'user'
            AND actor_user_id IS NOT NULL AND actor_agent_id IS NULL)
        OR (actor_type = 'agent'
            AND actor_user_id IS NULL AND actor_agent_id IS NOT NULL)
    )
);

CREATE INDEX audit_events_scope_time_idx
    ON audit_events (tenant_id, project_id, cluster_id, created_at DESC);
CREATE INDEX audit_events_request_id_idx ON audit_events (request_id);
