-- Everything AIOps stores: where its model requests go, and the sessions it
-- keeps. One migration because they arrive together and neither is useful
-- without the other — an endpoint nobody can start a session against, or a
-- session that has no model to drive it.

-- Where the AI assistant's model requests go, and with what credential.
--
-- Its own table rather than columns on platform_settings, for one reason that
-- is about the credential: this row holds an API Key, and platform_settings is
-- read on paths that have nothing to do with the assistant — every Agent
-- enrollment, every metrics install, every Cluster Terminal session. Those
-- reads have no business pulling a secret into memory, and a read shape that
-- excludes one column is a shape somebody eventually forgets to use.
--
-- A second reason follows from the first: this section has its own revision, so
-- an operator editing the model endpoint and one editing the Agent images do not
-- take each other's saves away. They are different sections of the same page and
-- were never one setting.
--
-- Only an OpenAI-compatible endpoint is described here. The target deployments
-- are private networks that need to point at a self-hosted inference service;
-- a vendor catalogue would be a compatibility surface to maintain forever in
-- exchange for nothing those deployments can reach.
--
-- The shipped state is one working preset with no credential: the public
-- DeepSeek endpoint, its Chat Completions protocol, and the assistant switched
-- on. That is a deliberate choice about where the first minute of a new
-- deployment goes — an administrator opens AIOps, is told the API Key is
-- missing, and fills in one field, rather than being handed an empty form and
-- having to work out an endpoint, a protocol, a model name and two token
-- budgets before anything answers.
--
-- Nothing leaves the deployment on the strength of this alone. Every turn needs
-- `ai.run` on a Cluster, and the endpoint refuses an unauthenticated request,
-- so a deployment that never fills in the key never reaches it. What the preset
-- does change is the default destination: an administrator who adds a key
-- without reading the address is sending cluster content to a public service.
-- The address is the first field on the form for that reason, and the same
-- warning stands on the page.
CREATE TABLE ai_model_settings (
    singleton boolean PRIMARY KEY DEFAULT true CHECK (singleton),
    enabled boolean NOT NULL DEFAULT true,
    -- Base URL of the OpenAI-compatible endpoint, without a trailing slash.
    -- The Server appends the operation path to it, so what is stored here is a
    -- prefix and not a complete request target.
    base_url text NOT NULL DEFAULT 'https://api.deepseek.com',
    -- Written by the operator. The Server ships no model list: it cannot know
    -- what a self-hosted service serves, and a guess published as a menu would
    -- be wrong in exactly the deployments this is for. The default is the model
    -- the preset endpoint serves, not a catalogue.
    model text NOT NULL DEFAULT 'deepseek-v4-flash',
    -- Wire protocol used by the configured endpoint. Responses is the native
    -- shape for long-running agent work and is the shipped default; Chat
    -- Completions remains available for a self-hosted OpenAI-compatible service
    -- that only serves that path. An endpoint that does not answer on the
    -- selected protocol fails the first turn as `model_rejected`, which the
    -- connectivity test on this page reports before anybody asks a question.
    api_protocol text NOT NULL DEFAULT 'responses' CHECK (
        api_protocol IN ('responses', 'chat_completions')
    ),
    -- Write-only from the API's side: reads report whether it is set, never the
    -- value. Empty is a supported state — a self-hosted service inside the same
    -- network may not authenticate at all — and is the shipped state: the
    -- preset above names an endpoint, and the credential is the one thing only
    -- the deployment can supply.
    --
    -- At rest this relies on the database's own protection. The Server adds no
    -- encryption of its own, which is a stated limitation rather than an
    -- oversight; see the Phase 4 design's open questions.
    api_key text NOT NULL DEFAULT '',
    -- OpenAI-compatible endpoints do not expose these limits consistently, so
    -- both are written by the operator. The runtime reserves the output budget
    -- out of the window on every request and compacts the conversation at a
    -- fraction of what is left.
    --
    -- That fraction is deliberately not a column: it is deployment policy, not
    -- a fact about the endpoint, and an absolute trigger stored here would be
    -- wrong the moment the same deployment is pointed at a model with a
    -- different window. It lives in the Server configuration file's `aiops`
    -- block instead.
    context_window_tokens integer NOT NULL DEFAULT 262144,
    max_output_tokens integer NOT NULL DEFAULT 32768,
    -- One model call's ceiling. Seconds, because every consumer of it counts in
    -- seconds and a duration string would be parsed identically everywhere.
    request_timeout_seconds integer NOT NULL DEFAULT 60,
    revision bigint NOT NULL DEFAULT 1 CHECK (revision > 0),
    updated_by_user_id uuid,
    updated_at timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT ai_model_settings_base_url_format CHECK (
        base_url = btrim(base_url) AND octet_length(base_url) <= 2048
    ),
    CONSTRAINT ai_model_settings_model_format CHECK (
        model = btrim(model) AND octet_length(model) <= 256
    ),
    CONSTRAINT ai_model_settings_api_key_length CHECK (
        octet_length(api_key) <= 4096
    ),
    CONSTRAINT ai_model_settings_request_timeout CHECK (
        request_timeout_seconds BETWEEN 5 AND 300
    ),
    CONSTRAINT ai_model_settings_context_window CHECK (
        context_window_tokens BETWEEN 16384 AND 4000000
    ),
    CONSTRAINT ai_model_settings_max_output CHECK (
        max_output_tokens BETWEEN 1024 AND 262144
        AND max_output_tokens < context_window_tokens
    ),
    -- Enabled without an endpoint or a model name is a state that can only
    -- fail later, at the point where somebody is waiting for an answer. It is
    -- refused here as well as in the Server so that neither side alone is what
    -- stands between an operator and a half-saved configuration.
    CONSTRAINT ai_model_settings_enabled_requires_endpoint CHECK (
        NOT enabled OR (base_url <> '' AND model <> '')
    )
);

INSERT INTO ai_model_settings (singleton) VALUES (true);

-- An AIOps session and the ordered trail of what it did.
--
-- A session is a long-lived object: a title, a series of turns, something the
-- operator comes back to days later. A turn is one question through to its
-- answer. An entry is one step of the trail.
--
-- Every step is written here first and pushed to whoever is watching second.
-- That order is what makes closing the window harmless, what lets a reconnect
-- resume from the last sequence it saw, and what makes a review afterwards see
-- the same thing the operator saw at the time.
--
-- Retention is short and separate from the audit trail. `audit_events` records
-- that a session ran and who asked for it, and keeps that for as long as the
-- deployment keeps audit; these rows hold cluster content — Events, metric
-- results, log excerpts — and are reclaimed much sooner.
CREATE TABLE ai_sessions (
    id uuid PRIMARY KEY,
    -- Who asked. It owns the conversation; cluster-derived entry bodies also
    -- require current authorization when the session APIs are implemented.
    initiator_user_id uuid NOT NULL,
    -- AIOps follows the desktop's current Tenant and Project, then uses one
    -- Cluster as its workspace. These identifiers deliberately have no foreign
    -- keys: retained conversations outlive deleted scope rows, while every API
    -- access re-resolves and reauthorizes the current scope.
    tenant_id uuid NOT NULL,
    project_id uuid NOT NULL,
    cluster_id uuid NOT NULL,
    title text NOT NULL CHECK (
        title = btrim(title) AND octet_length(title) BETWEEN 1 AND 200
    ),
    -- `working` means a turn is being driven right now. Sessions do not fail;
    -- turns do, and how the last one ended is recorded below.
    status text NOT NULL CHECK (status IN ('idle', 'working')),
    -- How far this session may go without asking, chosen by the operator in the
    -- composer and switchable whenever they like.
    --
    -- `ask` stops for a person before every write and before a Secret's values
    -- are read. `assisted` runs ordinary changes on its own and stops only at
    -- what ZKE already calls a sensitive operation — deletion, Secrets, RBAC,
    -- draining a Node, the protected Namespaces. `full` does everything the
    -- operator's permissions allow without stopping.
    --
    -- None of them grants anything: the ceiling is always that operator's own
    -- permissions, and the modes differ only in who presses the button. What
    -- they do change is how far a prompt injection out of a Pod log can reach,
    -- which is why the mode a turn ran under is part of its record.
    --
    -- A switch while a turn is running appends a `system` entry to the trail,
    -- so the record says which mode each part of the turn ran under rather
    -- than only which mode is set now.
    approval_mode text NOT NULL DEFAULT 'ask' CHECK (
        approval_mode IN ('ask', 'assisted', 'full')
    ),
    current_turn integer NOT NULL DEFAULT 0 CHECK (current_turn >= 0),
    -- The next sequence an appended entry will take. Allocated by the same
    -- statement that inserts the entry, so two concurrent appends cannot be
    -- given the same number, and an append with no turn running is refused by
    -- the row's own state rather than by whoever remembered to check.
    --
    -- Continuous across turns: "I have seen up to entry 37" has to mean one
    -- thing for the whole session, because that is what a reconnect resumes
    -- from.
    next_sequence integer NOT NULL DEFAULT 1 CHECK (next_sequence >= 1),
    -- How the most recent turn ended, so the session list can say so without
    -- reading anybody's trail.
    last_turn_status text NOT NULL DEFAULT '' CHECK (
        last_turn_status IN ('', 'succeeded', 'failed', 'canceled')
    ),
    -- Why it failed, as a classification the Server writes — never a message
    -- from a model or a cluster.
    last_turn_failure text NOT NULL DEFAULT '' CHECK (
        octet_length(last_turn_failure) <= 64
    ),
    created_at timestamptz NOT NULL,
    -- Retention is measured from here rather than from a stored expiry: a
    -- session is a living document, and one still being used should not
    -- disappear on a timer set when it was created. Reads and the reclamation
    -- both take the cutoff as a parameter, so changing the window does not mean
    -- rewriting rows.
    last_activity_at timestamptz NOT NULL,
    -- Library metadata only. Archiving hides a session from the active list;
    -- it never rewrites the append-only trajectory.
    archived_at timestamptz,
    CONSTRAINT ai_sessions_failure_only_when_failed CHECK (
        last_turn_failure = '' OR last_turn_status = 'failed'
    ),
    CONSTRAINT ai_sessions_working_has_a_turn CHECK (
        status = 'idle' OR current_turn >= 1
    )
);

-- The list one person sees inside the selected Tenant/Project/Cluster
-- workspace, most recently used first.
CREATE INDEX ai_sessions_initiator_idx
    ON ai_sessions (initiator_user_id, tenant_id, project_id, cluster_id, last_activity_at DESC, id DESC);
CREATE INDEX ai_sessions_activity_idx ON ai_sessions (last_activity_at);
CREATE INDEX ai_sessions_active_search_idx
    ON ai_sessions (initiator_user_id, tenant_id, project_id, cluster_id, last_activity_at DESC, id DESC)
    WHERE archived_at IS NULL;

-- One entry of a session's trail: append-only, ordered, never rewritten.
--
-- Entries are not modified independently. A turn that ends appends its last
-- entry; it does not go back and correct what it already said, because a trail
-- that can be edited afterwards is not evidence of anything. Permanently
-- deleting an archived session removes its whole trail through the parent
-- foreign key; an individual entry still has no update or delete API.
CREATE TABLE ai_session_events (
    session_id uuid NOT NULL REFERENCES ai_sessions (id) ON DELETE CASCADE,
    sequence integer NOT NULL CHECK (sequence >= 1),
    -- Which turn this entry belongs to. Rendering groups by it; the sequence
    -- still orders the session as a whole. A turn contains many steps, and
    -- which step an entry belongs to is carried inside `content` rather than as
    -- a column: nothing queries by it, and the loop that allocates it is the
    -- same one that writes the body.
    turn integer NOT NULL CHECK (turn >= 1),
    -- What the entry is. The vocabulary is fixed here as well as in the Server
    -- so an entry nobody can render cannot be stored.
    kind text NOT NULL CHECK (
        kind IN (
            'system', 'input', 'context', 'model', 'reasoning',
            'tool_call', 'tool_result', 'approval_request', 'approval_decision',
            'compaction', 'conclusion', 'error'
        )
    ),
    -- The entry's body, shaped by its kind. One column rather than a table per
    -- kind: they are read together, in order, always all of them, and the only
    -- thing anything does with the difference is render it.
    --
    -- Bounded because this is not a log system. The Server truncates well below
    -- this and marks the entry; the constraint is the backstop that keeps a
    -- writer which forgets from putting a whole Pod's logs in a row.
    content jsonb NOT NULL CHECK (
        jsonb_typeof(content) = 'object' AND octet_length(content::text) <= 65536
    ),
    -- Whether the Server cut the body down to fit. Rendering an excerpt as if
    -- it were the whole thing is how a trail starts lying quietly.
    truncated boolean NOT NULL DEFAULT false,
    occurred_at timestamptz NOT NULL,
    duration_ms integer NOT NULL DEFAULT 0 CHECK (duration_ms >= 0),
    PRIMARY KEY (session_id, sequence)
);

-- Attachments are intentionally text-only and small. They enter model context
-- as untrusted data, not executable files, and a session export can include
-- them without becoming a second object store or accepting arbitrary binary.
CREATE TABLE ai_session_attachments (
    id uuid PRIMARY KEY,
    session_id uuid NOT NULL REFERENCES ai_sessions (id) ON DELETE CASCADE,
    name text NOT NULL CHECK (
        name = btrim(name) AND octet_length(name) BETWEEN 1 AND 200
    ),
    media_type text NOT NULL CHECK (
        media_type IN ('text/plain', 'text/markdown', 'application/json', 'application/yaml')
    ),
    content text NOT NULL CHECK (
        octet_length(content) BETWEEN 1 AND 262144
    ),
    created_at timestamptz NOT NULL
);

CREATE INDEX ai_session_attachments_session_idx
    ON ai_session_attachments (session_id, created_at, id);
