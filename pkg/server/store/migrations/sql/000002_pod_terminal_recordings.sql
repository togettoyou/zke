CREATE TABLE pod_terminal_recordings (
    id uuid PRIMARY KEY,
    actor_user_id uuid NOT NULL,
    cluster_id uuid NOT NULL,
    -- The scope the Cluster sat in when the session ran, and the name it
    -- carried, resolved by the trigger below rather than supplied by the
    -- caller.
    --
    -- A recording is written from a cluster-scoped route -- `/clusters/:cluster_id/...`
    -- -- so the Tenant and Project are nowhere in the request; only the database
    -- knows them. Without them a recording survives its Cluster as an
    -- unresolvable UUID: it cannot be answered for by Project, and the rows
    -- that outlive a deleted Cluster are exactly the ones an investigation
    -- wants. Nullable for the same reason `audit_events` keeps them nullable --
    -- a recording written after its Cluster is gone records what could be known
    -- at that moment.
    tenant_id uuid,
    project_id uuid,
    cluster_name text,
    namespace text NOT NULL CHECK (
        namespace = btrim(namespace) AND octet_length(namespace) BETWEEN 1 AND 253
    ),
    pod_name text NOT NULL CHECK (
        pod_name = btrim(pod_name) AND octet_length(pod_name) BETWEEN 1 AND 253
    ),
    pod_uid text NOT NULL CHECK (
        pod_uid = btrim(pod_uid) AND octet_length(pod_uid) BETWEEN 1 AND 256
    ),
    container text NOT NULL CHECK (
        container = btrim(container) AND octet_length(container) BETWEEN 1 AND 253
    ),
    columns integer NOT NULL CHECK (columns BETWEEN 1 AND 65535),
    rows integer NOT NULL CHECK (rows BETWEEN 1 AND 65535),
    started_at timestamptz NOT NULL,
    ended_at timestamptz NOT NULL CHECK (ended_at >= started_at),
    expires_at timestamptz NOT NULL CHECK (expires_at > ended_at),
    result text NOT NULL CHECK (
        result IN ('succeeded', 'output_limit', 'timeout', 'canceled', 'failed')
    ),
    exit_code integer NOT NULL,
    output_bytes bigint NOT NULL CHECK (output_bytes >= 0),
    recording_bytes bigint NOT NULL CHECK (recording_bytes >= 0),
    truncated boolean NOT NULL,
    frames jsonb NOT NULL CHECK (jsonb_typeof(frames) = 'array')
);

-- The identity columns are intentionally not foreign keys. Terminal history
-- must neither prevent deleting a Cluster/User nor become readable through a
-- newly created object that happens to reuse a display name. Every read is
-- scoped by the immutable Cluster UUID and the HTTP authorization check.
CREATE INDEX pod_terminal_recordings_target_idx
    ON pod_terminal_recordings (cluster_id, namespace, pod_name, started_at DESC, id DESC);
CREATE INDEX pod_terminal_recordings_expiry_idx
    ON pod_terminal_recordings (expires_at);

-- The scope snapshot is filled here rather than at the call site, for the same
-- reason `fill_audit_event_names` exists: it makes the snapshot a property of
-- the table instead of something a writer has to remember. There is one writer
-- today, and this is what keeps a second one from silently producing rows with
-- no scope.
--
-- Only a Cluster that still resolves produces values, and an explicitly
-- supplied value is never overwritten.
CREATE FUNCTION fill_pod_terminal_recording_scope()
RETURNS trigger
LANGUAGE plpgsql
AS $$
DECLARE
    resolved record;
BEGIN
    IF NEW.tenant_id IS NULL OR NEW.project_id IS NULL OR NEW.cluster_name IS NULL THEN
        SELECT cluster.tenant_id, cluster.project_id, cluster.name
        INTO resolved
        FROM clusters AS cluster
        WHERE cluster.id = NEW.cluster_id;
        -- Guarded on FOUND rather than assigning straight into NEW: an
        -- unmatched SELECT INTO leaves its targets null, which would erase a
        -- value the caller did supply.
        IF FOUND THEN
            NEW.tenant_id := COALESCE(NEW.tenant_id, resolved.tenant_id);
            NEW.project_id := COALESCE(NEW.project_id, resolved.project_id);
            NEW.cluster_name := COALESCE(NEW.cluster_name, resolved.name);
        END IF;
    END IF;
    RETURN NEW;
END;
$$;

CREATE TRIGGER pod_terminal_recordings_fill_scope
BEFORE INSERT ON pod_terminal_recordings
FOR EACH ROW
EXECUTE FUNCTION fill_pod_terminal_recording_scope();
