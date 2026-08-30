-- Named MetricsQL expressions an operator keeps, and optionally shares.
--
-- Explore lets somebody write an expression instead of picking a named query
-- out of the catalogue. Writing one is the expensive part: the query that finds
-- the noisy Namespace, or the one that compares working set against the limit,
-- takes a few minutes to get right and is then needed again next week, by the
-- person who wrote it and usually by whoever is on call after them. This table
-- is where those go.
--
-- It stores text and nothing else. An expression is not a permission and not a
-- credential: it says which series to read, never which Cluster to read them
-- from. The Cluster comes from the target chosen in the Console at the moment
-- the query runs, and the Server rewrites every selector to name it — so a
-- saved query written by one person and run by another describes whichever
-- Cluster the reader was already allowed to read, and a `zke_cluster_id` filter
-- saved into the text is replaced rather than obeyed. That is what makes
-- sharing these safe.
--
-- Scoped to a Project rather than to a Cluster or globally. A Cluster is too
-- narrow — the same expression is asked of every Cluster in a Project — and
-- global is too wide, because a Project's queries are written against what that
-- Project runs and would be noise in everybody else's picker.
CREATE TABLE metrics_saved_queries (
    id uuid PRIMARY KEY,
    -- Deleting the Project takes its saved queries with it. This is one of the
    -- few tables that cascades rather than being named in deleteProjectTree:
    -- it is a leaf with no children, no audit value and nothing that has to
    -- outlive the scope it describes, so there is nothing for an explicit
    -- ordered deletion to get right.
    project_id uuid NOT NULL REFERENCES projects (id) ON DELETE CASCADE,
    -- Who wrote it. Nullable, and set to null when that account is deleted:
    -- a query shared into a Project belongs to the Project's library from the
    -- moment it is shared, and losing the team's saved queries because their
    -- author left would be a surprising way to lose them. Private queries are
    -- removed with their author instead, by the user deletion path — a private
    -- row nobody can select is not worth keeping.
    owner_user_id uuid REFERENCES users (id) ON DELETE SET NULL,
    -- `private` is visible to its owner alone; `project` is visible to
    -- everyone who may read metrics in the Project.
    --
    -- Two values rather than a share list. A saved query is a piece of text
    -- worth a few minutes of somebody's time, and per-entry access control
    -- would be a second permission system maintained beside the real one for
    -- something that grants no access to anything.
    visibility text NOT NULL CHECK (visibility IN ('private', 'project')),
    -- What it is called in the picker.
    name text NOT NULL CHECK (
        name = btrim(name) AND octet_length(name) BETWEEN 1 AND 100
    ),
    description text NOT NULL DEFAULT '' CHECK (
        octet_length(description) <= 500
    ),
    -- The expression as written. The upper bound is the same one the query
    -- guard enforces, so a row that exists is a row the guard was willing to
    -- rewrite when it was saved.
    expression text NOT NULL CHECK (
        expression = btrim(expression) AND octet_length(expression) BETWEEN 1 AND 4096
    ),
    created_at timestamptz NOT NULL,
    updated_at timestamptz NOT NULL
);

-- Names are unique where they are seen together, and only there.
--
-- Two people may each keep a private 「内存用量」 without colliding, because
-- neither can see the other's. A name shared into the Project has to be unique
-- across it: the picker is one list, and two entries reading the same would be
-- a coin flip. Case-insensitive for the reason names usually are — 「CPU」 and
-- 「cpu」 in one list is a trap, not a distinction.
CREATE UNIQUE INDEX metrics_saved_queries_shared_name_idx
    ON metrics_saved_queries (project_id, lower(name))
    WHERE visibility = 'project';
CREATE UNIQUE INDEX metrics_saved_queries_private_name_idx
    ON metrics_saved_queries (project_id, owner_user_id, lower(name))
    WHERE visibility = 'private';

-- The listing: one Project's shared entries plus the reader's own, in the order
-- the picker shows them. Ordering is part of the index because the whole list
-- is read at once — it is a picker, not a paged table.
CREATE INDEX metrics_saved_queries_listing_idx
    ON metrics_saved_queries (project_id, lower(name), id);
