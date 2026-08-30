-- A compact turn ledger makes quota admission atomic and keeps evaluation from
-- having to infer historical turn outcomes from presentation-oriented events.
CREATE TABLE ai_turn_runs (
    session_id uuid NOT NULL REFERENCES ai_sessions (id) ON DELETE CASCADE,
    turn integer NOT NULL CHECK (turn >= 1),
    started_at timestamptz NOT NULL,
    finished_at timestamptz,
    status text NOT NULL CHECK (status IN ('running', 'succeeded', 'failed', 'canceled')),
    failure text NOT NULL DEFAULT '' CHECK (octet_length(failure) <= 64),
    PRIMARY KEY (session_id, turn),
    CONSTRAINT ai_turn_runs_finished_state CHECK (
        (status = 'running' AND finished_at IS NULL AND failure = '') OR
        (status <> 'running' AND finished_at IS NOT NULL)
    ),
    CONSTRAINT ai_turn_runs_failure_state CHECK (
        failure = '' OR status = 'failed'
    )
);

CREATE INDEX ai_turn_runs_started_idx ON ai_turn_runs (started_at DESC);

-- Earlier migrations did not keep a turn ledger. Preserve those conversations
-- in evaluation with the strongest outcome the record can prove. The session
-- row already says how its most recent turn ended, so that turn is read from
-- there rather than guessed; older turns are read from the entries they left.
--
-- A cancellation also writes an error entry before it ends the turn, so an
-- error alone does not mean the turn failed: 'session_ended' is the operator
-- stopping the run. A classification belongs to a failed turn and to nothing
-- else, which is what the table's own constraints require.
INSERT INTO ai_turn_runs (session_id, turn, started_at, finished_at, status, failure)
SELECT restored.session_id,
       restored.turn,
       restored.started_at,
       CASE WHEN restored.status = 'running' THEN NULL ELSE restored.finished_at END,
       restored.status,
       CASE WHEN restored.status = 'failed' THEN COALESCE(restored.failure, '') ELSE '' END
FROM (
    SELECT input.session_id AS session_id,
           input.turn AS turn,
           input.occurred_at AS started_at,
           COALESCE(MAX(event.occurred_at), input.occurred_at) AS finished_at,
           CASE
               WHEN session.status = 'working' AND input.turn = session.current_turn
                   THEN 'running'
               WHEN input.turn = session.current_turn AND session.last_turn_status <> ''
                   THEN session.last_turn_status
               WHEN BOOL_OR(event.kind = 'conclusion') THEN 'succeeded'
               WHEN BOOL_OR(
                   event.kind = 'error'
                   AND event.content->>'failure' IS DISTINCT FROM 'session_ended'
               ) THEN 'failed'
               ELSE 'canceled'
           END AS status,
           CASE
               WHEN input.turn = session.current_turn AND session.last_turn_status <> ''
                   THEN session.last_turn_failure
               ELSE (ARRAY_AGG(event.content->>'failure' ORDER BY event.sequence DESC)
                        FILTER (WHERE event.kind = 'error'))[1]
           END AS failure
    FROM ai_session_events AS input
    JOIN ai_sessions AS session ON session.id = input.session_id
    JOIN ai_session_events AS event
      ON event.session_id = input.session_id AND event.turn = input.turn
    WHERE input.kind = 'input'
    GROUP BY input.session_id, input.turn, input.occurred_at, session.status,
             session.current_turn, session.last_turn_status, session.last_turn_failure
) AS restored;

-- A turn's user feedback is mutable product data, not part of the append-only
-- execution trail. Keeping it separate lets an operator revise their judgement
-- without rewriting the record of what AIOps actually did.
CREATE TABLE ai_turn_feedback (
    session_id uuid NOT NULL REFERENCES ai_sessions (id) ON DELETE CASCADE,
    turn integer NOT NULL CHECK (turn >= 1),
    initiator_user_id uuid NOT NULL,
    rating text NOT NULL CHECK (rating IN ('helpful', 'not_helpful')),
    outcome text NOT NULL CHECK (outcome IN ('resolved', 'unresolved', 'unsure')),
    reasons text[] NOT NULL DEFAULT '{}',
    comment text NOT NULL DEFAULT '' CHECK (octet_length(comment) <= 2000),
    created_at timestamptz NOT NULL,
    updated_at timestamptz NOT NULL,
    PRIMARY KEY (session_id, turn),
    FOREIGN KEY (session_id, turn) REFERENCES ai_turn_runs (session_id, turn) ON DELETE CASCADE,
    CONSTRAINT ai_turn_feedback_reasons_known CHECK (
        reasons <@ ARRAY[
            'inaccurate', 'insufficient_evidence', 'incomplete',
            'unsafe', 'hard_to_follow', 'other'
        ]::text[]
    )
);

CREATE INDEX ai_turn_feedback_evaluation_idx
    ON ai_turn_feedback (initiator_user_id, updated_at DESC);
