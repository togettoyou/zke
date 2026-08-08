CREATE TABLE pod_terminal_recordings (
    id uuid PRIMARY KEY,
    actor_user_id uuid NOT NULL,
    cluster_id uuid NOT NULL,
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
