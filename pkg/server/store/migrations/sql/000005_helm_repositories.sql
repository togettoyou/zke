-- Helm chart repositories.
--
-- A chart has to come from somewhere, and ZKE will not take one from whoever is
-- asking: an operator installing a release chooses a chart out of a catalogue a
-- platform administrator put there. That is what this table is — the catalogue,
-- not a cache. Index documents and chart archives are fetched on demand and
-- held in memory by the Server; nothing about a chart is stored here.
--
-- The catalogue is platform-wide rather than per Tenant or per Project. A
-- repository is a source of software, not a piece of anyone's infrastructure,
-- and duplicating the same public repository into every Project would mean
-- every Project separately deciding whether to trust it. Which Clusters a
-- release may be installed into is decided by the Cluster permissions, where it
-- belongs, and not by which repository the chart came from.
CREATE TABLE helm_repositories (
    id uuid PRIMARY KEY,
    -- The name an operator picks it out by. Unique case-insensitively, because
    -- two repositories differing only in capitalisation are a trap rather than
    -- a distinction.
    name text NOT NULL CHECK (
        name = btrim(name) AND octet_length(name) BETWEEN 1 AND 100
    ),
    description text NOT NULL DEFAULT '' CHECK (
        octet_length(description) <= 500
    ),
    -- The repository's base URL, the one an `index.yaml` sits under. Only
    -- http and https are accepted, and the Server checks the scheme again
    -- before it fetches: a URL is a place this Server will make a request to,
    -- so it is validated where it is used and not only where it is stored.
    url text NOT NULL CHECK (
        url = btrim(url) AND octet_length(url) BETWEEN 8 AND 2000
        AND (url LIKE 'http://%' OR url LIKE 'https://%')
    ),
    -- Credentials for a private repository. Stored as written and never
    -- returned by any API: the update path takes a null to mean "keep what is
    -- there", exactly as the AI model API key does, so a Console that never
    -- receives the value can still save the rest of the row.
    username text NOT NULL DEFAULT '' CHECK (octet_length(username) <= 200),
    password text NOT NULL DEFAULT '' CHECK (octet_length(password) <= 1000),
    -- A private repository behind a certificate this Server does not otherwise
    -- trust. Empty means the system trust store decides.
    ca_certificate_pem text NOT NULL DEFAULT '' CHECK (
        octet_length(ca_certificate_pem) <= 65536
    ),
    -- Skipping verification is a deliberate, visible choice rather than a
    -- silent fallback, so it is a column an auditor can select on.
    insecure_skip_tls_verify boolean NOT NULL DEFAULT false,
    -- A repository can be turned off without losing its configuration. A
    -- disabled repository is not offered for new installs; releases already
    -- installed from it are unaffected, because a release carries its chart.
    enabled boolean NOT NULL DEFAULT true,
    created_by_user_id uuid REFERENCES users (id) ON DELETE SET NULL,
    updated_by_user_id uuid REFERENCES users (id) ON DELETE SET NULL,
    created_at timestamptz NOT NULL,
    updated_at timestamptz NOT NULL
);

CREATE UNIQUE INDEX helm_repositories_name_key
    ON helm_repositories (lower(name));
