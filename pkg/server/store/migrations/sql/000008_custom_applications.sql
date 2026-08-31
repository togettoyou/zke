-- Project-scoped links that become first-class applications on the ZKE desktop.
--
-- The Server stores metadata only. It never fetches either URL, which keeps an
-- administrator-provided icon or application address from becoming an SSRF
-- primitive. The Console loads both in the operator's browser.
CREATE TABLE custom_applications (
    id uuid PRIMARY KEY,
    project_id uuid NOT NULL REFERENCES projects (id) ON DELETE CASCADE,
    created_by_user_id uuid REFERENCES users (id) ON DELETE SET NULL,
    name text NOT NULL CHECK (
        name = btrim(name) AND octet_length(name) BETWEEN 1 AND 80
    ),
    description text NOT NULL DEFAULT '' CHECK (
        description = btrim(description) AND octet_length(description) <= 500
    ),
    url text NOT NULL CHECK (
        url = btrim(url) AND octet_length(url) BETWEEN 1 AND 2048
    ),
    logo_url text NOT NULL DEFAULT '' CHECK (
        logo_url = btrim(logo_url) AND octet_length(logo_url) <= 65536
    ),
    idempotency_key text NOT NULL CHECK (
        idempotency_key = btrim(idempotency_key)
        AND length(idempotency_key) BETWEEN 16 AND 128
    ),
    created_at timestamptz NOT NULL,
    updated_at timestamptz NOT NULL,
    CONSTRAINT custom_applications_creator_idempotency_unique
        UNIQUE (created_by_user_id, project_id, idempotency_key)
);

-- A launcher cannot distinguish two applications with the same caption. The
-- uniqueness is therefore case-insensitive within one Project.
CREATE UNIQUE INDEX custom_applications_project_name_idx
    ON custom_applications (project_id, lower(name));

CREATE INDEX custom_applications_listing_idx
    ON custom_applications (project_id, lower(name), id);
