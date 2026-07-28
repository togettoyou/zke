CREATE TABLE tenants (
    id uuid PRIMARY KEY,
    name text NOT NULL CHECK (
        name = btrim(name)
        AND octet_length(name) BETWEEN 1 AND 253
    ),
    status text NOT NULL CHECK (status IN ('active', 'suspended')),
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now()
);

-- Tenant names are unique, compared without case, and the same index orders the
-- listing.
--
-- A Tenant is the top of the scope tree and the label an operator picks a
-- working scope by; two of them called "Acme" are two things nobody can tell
-- apart in a picker, an audit row or a support conversation. Case is ignored
-- because everything else about a Tenant name already ignores it: the listing
-- orders by lower(name) and the search matches lower(name), so "Acme" and
-- "acme" were already the same name everywhere except here.
--
-- Uniqueness spans both statuses. Suspension is reversible and destroys
-- nothing, so a suspended Tenant keeps its name — releasing it would let
-- someone else take it and collide when the original is resumed. A Tenant that
-- is finished is deleted, and deletion frees the name by removing the row.
--
-- No `id` tiebreaker column: with lower(name) unique the ordering is already
-- total, so a second index column would never be reached.
CREATE UNIQUE INDEX tenants_name_unique_idx ON tenants (lower(name));
CREATE INDEX tenants_status_idx ON tenants (status);

CREATE TABLE projects (
    id uuid PRIMARY KEY,
    tenant_id uuid NOT NULL REFERENCES tenants (id),
    name text NOT NULL CHECK (
        name = btrim(name)
        AND octet_length(name) BETWEEN 1 AND 253
    ),
    status text NOT NULL CHECK (status IN ('active', 'suspended')),
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    UNIQUE (tenant_id, id)
);

CREATE INDEX projects_tenant_id_idx ON projects (tenant_id);
-- Project names are unique inside their Tenant, compared without case, and the
-- same index orders the listing.
--
-- Scoped to the Tenant rather than global: Projects are named inside an
-- organization, and two tenants both having a "platform" is normal. Case is
-- ignored for the same reason as Tenants — the listing and the search already
-- ignore it.
--
-- Uniqueness spans both statuses, for the same reason as Tenants: suspension is
-- reversible, so the name stays held; deletion removes the row and with it the
-- name.
CREATE UNIQUE INDEX projects_tenant_name_unique_idx
    ON projects (tenant_id, lower(name));
CREATE INDEX projects_status_idx ON projects (status);

CREATE TABLE users (
    id uuid PRIMARY KEY,
    username_normalized text NOT NULL UNIQUE
        CHECK (username_normalized = lower(btrim(username_normalized)))
        CHECK (length(username_normalized) > 0),
    display_name text NOT NULL CHECK (length(btrim(display_name)) > 0),
    password_hash text NOT NULL CHECK (length(password_hash) > 0),
    status text NOT NULL CHECK (status IN ('active', 'locked', 'disabled')),
    failed_login_count integer NOT NULL DEFAULT 0 CHECK (failed_login_count >= 0),
    locked_at timestamptz,
    lock_expires_at timestamptz,
    password_changed_at timestamptz NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT users_lock_shape CHECK (
        (status = 'locked' AND locked_at IS NOT NULL AND lock_expires_at IS NOT NULL)
        OR (status <> 'locked' AND locked_at IS NULL AND lock_expires_at IS NULL)
    )
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
-- Role bindings are listed oldest-first by creation time.
CREATE INDEX role_bindings_created_at_idx ON role_bindings (created_at, id);
CREATE INDEX users_status_idx ON users (status);

CREATE TABLE clusters (
    id uuid PRIMARY KEY,
    tenant_id uuid NOT NULL REFERENCES tenants (id),
    project_id uuid NOT NULL,
    name text NOT NULL CHECK (
        name = btrim(name)
        AND octet_length(name) BETWEEN 1 AND 253
    ),
    -- 'revoked' is gone: a Cluster that is finished is deleted, and one that
    -- is merely frozen is suspended and can come back.
    status text NOT NULL CHECK (status IN ('pending', 'active', 'suspended')),
    last_seen_at timestamptz,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT clusters_project_scope_fk
        FOREIGN KEY (tenant_id, project_id) REFERENCES projects (tenant_id, id),
    UNIQUE (tenant_id, project_id, id)
);

CREATE INDEX clusters_project_scope_idx ON clusters (tenant_id, project_id);
-- Clusters are listed by name inside a project.
CREATE INDEX clusters_project_name_idx ON clusters (project_id, name, id);
-- Cluster names are unique inside their Project, compared without case.
--
-- Total rather than partial: deletion removes the row, so a deleted Cluster
-- releases its name by disappearing rather than by being excluded here.
CREATE UNIQUE INDEX clusters_project_name_unique_idx
    ON clusters (project_id, lower(name));
CREATE INDEX clusters_status_idx ON clusters (status);

CREATE TABLE agents (
    id uuid PRIMARY KEY,
    tenant_id uuid NOT NULL REFERENCES tenants (id),
    project_id uuid NOT NULL,
    cluster_id uuid NOT NULL,
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

CREATE UNIQUE INDEX agents_active_cluster_unique
    ON agents (cluster_id)
    WHERE lifecycle_status <> 'revoked';

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

-- Suspending a Cluster drops its Agent immediately rather than waiting for the
-- next heartbeat to notice. The Agent's identity is untouched, so resuming the
-- Cluster lets it reconnect with the credential it already holds.
CREATE FUNCTION notify_cluster_suspension()
RETURNS trigger
LANGUAGE plpgsql
AS $$
BEGIN
    IF OLD.status <> 'suspended' AND NEW.status = 'suspended' THEN
        PERFORM pg_notify(
            'zke_agent_connection_revocations',
            json_build_object(
                'cluster_id', NEW.id,
                'reason', 'scope_suspended'
            )::text
        );
    END IF;
    RETURN NEW;
END;
$$;

CREATE TRIGGER clusters_notify_suspension
AFTER UPDATE OF status ON clusters
FOR EACH ROW
EXECUTE FUNCTION notify_cluster_suspension();

CREATE FUNCTION notify_project_suspension()
RETURNS trigger
LANGUAGE plpgsql
AS $$
BEGIN
    IF OLD.status <> 'suspended' AND NEW.status = 'suspended' THEN
        PERFORM pg_notify(
            'zke_agent_connection_revocations',
            json_build_object(
                'project_id', NEW.id,
                'reason', 'scope_suspended'
            )::text
        );
    END IF;
    RETURN NEW;
END;
$$;

CREATE TRIGGER projects_notify_suspension
AFTER UPDATE OF status ON projects
FOR EACH ROW
EXECUTE FUNCTION notify_project_suspension();

CREATE FUNCTION notify_tenant_suspension()
RETURNS trigger
LANGUAGE plpgsql
AS $$
BEGIN
    IF OLD.status <> 'suspended' AND NEW.status = 'suspended' THEN
        PERFORM pg_notify(
            'zke_agent_connection_revocations',
            json_build_object(
                'tenant_id', NEW.id,
                'reason', 'scope_suspended'
            )::text
        );
    END IF;
    RETURN NEW;
END;
$$;

CREATE TRIGGER tenants_notify_suspension
AFTER UPDATE OF status ON tenants
FOR EACH ROW
EXECUTE FUNCTION notify_tenant_suspension();

-- Deleting an Agent row ends its connection at once. Every deletion that can
-- reach a live Agent — Cluster, Project or Tenant — passes through here, so one
-- trigger covers all three.
CREATE FUNCTION notify_agent_deletion()
RETURNS trigger
LANGUAGE plpgsql
AS $$
BEGIN
    PERFORM pg_notify(
        'zke_agent_connection_revocations',
        json_build_object('agent_id', OLD.id)::text
    );
    RETURN OLD;
END;
$$;

CREATE TRIGGER agents_notify_deletion
AFTER DELETE ON agents
FOR EACH ROW
EXECUTE FUNCTION notify_agent_deletion();

CREATE TABLE enrollments (
    id uuid PRIMARY KEY,
    tenant_id uuid NOT NULL REFERENCES tenants (id),
    project_id uuid NOT NULL,
    cluster_id uuid,
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
    CONSTRAINT enrollments_cluster_scope_fk
        FOREIGN KEY (tenant_id, project_id, cluster_id)
        REFERENCES clusters (tenant_id, project_id, id),
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
-- Cluster enrollments are listed newest-first inside a project.
CREATE INDEX enrollments_project_created_at_idx
    ON enrollments (project_id, created_at DESC, id);
CREATE INDEX enrollments_cluster_id_idx
    ON enrollments (cluster_id)
    WHERE cluster_id IS NOT NULL;
CREATE INDEX enrollments_active_expiry_idx
    ON enrollments (expires_at)
    WHERE consumed_at IS NULL AND revoked_at IS NULL;
-- An outstanding enrollment claims the Cluster name it will create, so two of
-- them in a Project cannot claim the same one. Without this, both are issued
-- happily and whichever Agent arrives second fails against the Cluster name
-- index — far from the operator who chose the name.
--
-- Consumed and revoked enrollments are excluded because they claim nothing any
-- more. Expiry cannot be a predicate here (`now()` is not immutable), so
-- CreateEnrollment revokes an expired holder before inserting, which is what
-- lets a name become reusable once its token has lapsed.
CREATE UNIQUE INDEX enrollments_project_cluster_name_unique_idx
    ON enrollments (project_id, lower(cluster_name))
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

-- The audit trail outlives what it describes.
--
-- Every identifier here is a plain uuid with no foreign key, which is the whole
-- point: Tenants, Projects, Clusters and users are really deleted, and the
-- record of who deleted them has to survive that. A foreign key would force the
-- opposite — either the deletion fails, or the evidence goes with it.
--
-- Because the ids stop resolving the moment their subject is gone, each one is
-- accompanied by the name that subject carried when the event was written. The
-- id stays the thing to correlate on; the name is what makes a row still
-- readable a year later. Agents have no name, so they are recorded by id alone.
CREATE TABLE audit_events (
    id uuid PRIMARY KEY,
    actor_type text NOT NULL CHECK (actor_type IN ('user', 'agent', 'system')),
    actor_user_id uuid,
    actor_user_name text,
    actor_agent_id uuid,
    scope_type text NOT NULL CHECK (scope_type IN ('global', 'tenant', 'project', 'cluster')),
    tenant_id uuid,
    tenant_name text,
    project_id uuid,
    project_name text,
    cluster_id uuid,
    cluster_name text,
    action text NOT NULL CHECK (length(btrim(action)) > 0),
    target_type text NOT NULL CHECK (length(btrim(target_type)) > 0),
    target_id uuid,
    target_name text,
    result text NOT NULL CHECK (result IN ('succeeded', 'failed', 'denied')),
    request_id text NOT NULL CHECK (length(btrim(request_id)) > 0),
    created_at timestamptz NOT NULL DEFAULT now(),
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
-- Audit events are listed newest-first; this serves the globally visible
-- ordering that audit_events_scope_time_idx does not cover.
CREATE INDEX audit_events_created_at_idx ON audit_events (created_at DESC, id DESC);

-- The name snapshots are filled in here rather than at each of the two dozen
-- statements that write an audit event.
--
-- Every one of those would otherwise have to remember to join four more tables,
-- and the one that forgot would produce rows that look fine until the subject is
-- deleted and the name turns out to be missing — visible only long after the
-- evidence is gone. Resolving them in one trigger makes the snapshot a property
-- of the table instead of a convention, and a new call site gets it for free.
--
-- Only ids that still resolve produce a name. A caller that deletes its subject
-- before writing the audit row therefore leaves the name null, which is the
-- correct record of what could be known at that moment — and the reason the
-- deletion paths write their audit event first.
--
-- Users are recorded by username rather than display name: the username is the
-- identity being held to account, it is unique, and the display name is free to
-- change or repeat.
CREATE FUNCTION fill_audit_event_names()
RETURNS trigger
LANGUAGE plpgsql
AS $$
BEGIN
    IF NEW.actor_user_id IS NOT NULL AND NEW.actor_user_name IS NULL THEN
        SELECT username_normalized INTO NEW.actor_user_name
        FROM users WHERE id = NEW.actor_user_id;
    END IF;
    IF NEW.tenant_id IS NOT NULL AND NEW.tenant_name IS NULL THEN
        SELECT name INTO NEW.tenant_name FROM tenants WHERE id = NEW.tenant_id;
    END IF;
    IF NEW.project_id IS NOT NULL AND NEW.project_name IS NULL THEN
        SELECT name INTO NEW.project_name FROM projects WHERE id = NEW.project_id;
    END IF;
    IF NEW.cluster_id IS NOT NULL AND NEW.cluster_name IS NULL THEN
        SELECT name INTO NEW.cluster_name FROM clusters WHERE id = NEW.cluster_id;
    END IF;
    IF NEW.target_id IS NOT NULL AND NEW.target_name IS NULL THEN
        CASE NEW.target_type
            WHEN 'user' THEN
                SELECT username_normalized INTO NEW.target_name
                FROM users WHERE id = NEW.target_id;
            WHEN 'tenant' THEN
                SELECT name INTO NEW.target_name
                FROM tenants WHERE id = NEW.target_id;
            WHEN 'project' THEN
                SELECT name INTO NEW.target_name
                FROM projects WHERE id = NEW.target_id;
            WHEN 'cluster' THEN
                SELECT name INTO NEW.target_name
                FROM clusters WHERE id = NEW.target_id;
            WHEN 'enrollment' THEN
                SELECT cluster_name INTO NEW.target_name
                FROM enrollments WHERE id = NEW.target_id;
            ELSE
                -- Sessions, RoleBindings, Agents and credentials have no name an
                -- operator would recognise; their id is the whole identity.
                NULL;
        END CASE;
    END IF;
    RETURN NEW;
END;
$$;

CREATE TRIGGER audit_events_fill_names
BEFORE INSERT ON audit_events
FOR EACH ROW
EXECUTE FUNCTION fill_audit_event_names();

CREATE TABLE tenant_creation_requests (
    id uuid PRIMARY KEY,
    actor_user_id uuid NOT NULL REFERENCES users (id),
    idempotency_key text NOT NULL,
    requested_name text NOT NULL,
    tenant_id uuid NOT NULL UNIQUE REFERENCES tenants (id),
    created_at timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT tenant_creation_requests_key_format CHECK (
        idempotency_key = btrim(idempotency_key)
        AND length(idempotency_key) BETWEEN 16 AND 128
    ),
    UNIQUE (actor_user_id, idempotency_key)
);

CREATE TABLE project_creation_requests (
    id uuid PRIMARY KEY,
    actor_user_id uuid NOT NULL REFERENCES users (id),
    tenant_id uuid NOT NULL REFERENCES tenants (id),
    idempotency_key text NOT NULL,
    requested_name text NOT NULL,
    project_id uuid NOT NULL UNIQUE,
    created_at timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT project_creation_requests_project_fk
        FOREIGN KEY (tenant_id, project_id) REFERENCES projects (tenant_id, id),
    CONSTRAINT project_creation_requests_key_format CHECK (
        idempotency_key = btrim(idempotency_key)
        AND length(idempotency_key) BETWEEN 16 AND 128
    ),
    UNIQUE (actor_user_id, tenant_id, idempotency_key)
);
