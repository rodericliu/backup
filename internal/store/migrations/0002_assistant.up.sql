-- Conversations with the read-only AI assistant. Sessions are private to
-- the user who created them.
CREATE TABLE assistant_sessions (
    id         bigserial PRIMARY KEY,
    user_id    bigint      NOT NULL REFERENCES users (id) ON DELETE CASCADE,
    title      text        NOT NULL DEFAULT '',
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX assistant_sessions_user ON assistant_sessions (user_id, updated_at DESC);

-- role is user or assistant. tool_calls holds the tools an assistant
-- message called, with their results, to show them and to replay them to
-- the model in later turns.
CREATE TABLE assistant_messages (
    id            bigserial PRIMARY KEY,
    session_id    bigint      NOT NULL REFERENCES assistant_sessions (id) ON DELETE CASCADE,
    role          text        NOT NULL,
    content       text        NOT NULL DEFAULT '',
    reasoning     text        NOT NULL DEFAULT '',
    tool_calls    jsonb       NOT NULL DEFAULT '[]',
    model         text        NOT NULL DEFAULT '',
    error         text        NOT NULL DEFAULT '',
    input_tokens  bigint      NOT NULL DEFAULT 0,
    output_tokens bigint      NOT NULL DEFAULT 0,
    created_at    timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX assistant_messages_session ON assistant_messages (session_id, id);
