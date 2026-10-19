-- 0001_init.sql: 方案 A 核心库表结构
-- 包含用户、群组、会话房间、模型全量快照、问答轮次、消息片段与审计事件

-- 1. 用户维度表
CREATE TABLE users (
    id          INTEGER PRIMARY KEY AUTOINCREMENT,
    app_id      TEXT    NOT NULL,
    openid      TEXT    NOT NULL,
    nickname    TEXT    NOT NULL DEFAULT '',
    avatar      TEXT    NOT NULL DEFAULT '',
    created_at  INTEGER NOT NULL,
    updated_at  INTEGER NOT NULL,
    UNIQUE (app_id, openid)
);
CREATE INDEX idx_users_openid ON users (app_id, openid);

-- 2. 群聊维度表
CREATE TABLE groups (
    id           INTEGER PRIMARY KEY AUTOINCREMENT,
    app_id       TEXT    NOT NULL,
    group_openid TEXT    NOT NULL,
    name         TEXT    NOT NULL DEFAULT '',
    created_at   INTEGER NOT NULL,
    updated_at   INTEGER NOT NULL,
    UNIQUE (app_id, group_openid)
);
CREATE INDEX idx_groups_openid ON groups (app_id, group_openid);

-- 3. 会话房间表（采用方案 A：直接用 scene_id 对应 user_openid 或 group_openid）
CREATE TABLE conversations (
    id         INTEGER PRIMARY KEY AUTOINCREMENT,
    app_id     TEXT    NOT NULL,
    scene      TEXT    NOT NULL,                -- 'c2c' | 'group'
    scene_id   TEXT    NOT NULL,                -- c2c: user_openid；group: group_openid
    created_at INTEGER NOT NULL,
    updated_at INTEGER NOT NULL,
    UNIQUE (app_id, scene, scene_id)
);

-- 4. 模型调用全量快照表（保留原始 JSON 与性能开销）
CREATE TABLE llm_requests (
    id                 INTEGER PRIMARY KEY AUTOINCREMENT,
    trace_id           TEXT    NOT NULL,
    conversation_id    INTEGER REFERENCES conversations(id) ON DELETE CASCADE,
    provider           TEXT    NOT NULL,        -- 'openaichat' | 'anthropic' | 'fake' 等
    model              TEXT    NOT NULL,
    temperature        REAL,
    prompt_tokens      INTEGER NOT NULL DEFAULT 0,
    completion_tokens  INTEGER NOT NULL DEFAULT 0,
    total_tokens       INTEGER NOT NULL DEFAULT 0,
    duration_ms        INTEGER NOT NULL DEFAULT 0,
    status             TEXT    NOT NULL,        -- 'success' | 'failed'
    error_message      TEXT    NOT NULL DEFAULT '',
    raw_request        TEXT    NOT NULL,        -- 完整请求 JSON（含上下文、参数）
    raw_response       TEXT    NOT NULL DEFAULT '', -- 完整响应 JSON（含回答、token、tool）
    created_at         INTEGER NOT NULL
);
CREATE INDEX idx_llm_requests_trace ON llm_requests (trace_id);
CREATE INDEX idx_llm_requests_conv ON llm_requests (conversation_id, created_at DESC);

-- 5. 问答轮次表（挂载 llm_request_id，双向可溯）
CREATE TABLE turns (
    id              INTEGER PRIMARY KEY AUTOINCREMENT,
    conversation_id INTEGER NOT NULL REFERENCES conversations(id) ON DELETE CASCADE,
    user_id         INTEGER REFERENCES users(id),
    inbound_msg_id  TEXT    NOT NULL,
    status          TEXT    NOT NULL,           -- 'processing' | 'ok' | 'failed' | 'filtered'
    llm_request_id  INTEGER REFERENCES llm_requests(id) ON DELETE SET NULL,
    user_text       TEXT    NOT NULL,
    reply_text      TEXT    NOT NULL DEFAULT '',
    trace_id        TEXT    NOT NULL,
    started_at      INTEGER NOT NULL,
    finished_at     INTEGER,
    UNIQUE (conversation_id, inbound_msg_id)
);
CREATE INDEX idx_turns_window ON turns (conversation_id, status, id DESC);
CREATE INDEX idx_turns_llm_req ON turns (llm_request_id);

-- 6. 上下文片段表（用于快速拼装多轮 prompt）
CREATE TABLE messages (
    id          INTEGER PRIMARY KEY AUTOINCREMENT,
    turn_id     INTEGER NOT NULL REFERENCES turns(id) ON DELETE CASCADE,
    seq_in_turn INTEGER NOT NULL,
    role        TEXT    NOT NULL,               -- 'user' | 'assistant' | 'tool'
    content     TEXT    NOT NULL,
    created_at  INTEGER NOT NULL
);
CREATE INDEX idx_messages_turn ON messages (turn_id, seq_in_turn);

-- 7. 系统审计事件表
CREATE TABLE events (
    id              INTEGER PRIMARY KEY AUTOINCREMENT,
    created_at      INTEGER NOT NULL,
    level           TEXT    NOT NULL,
    kind            TEXT    NOT NULL,
    app_id          TEXT    NOT NULL DEFAULT '',
    trace_id        TEXT    NOT NULL DEFAULT '',
    conversation_id INTEGER,
    turn_id         INTEGER,
    platform_msg_id TEXT    NOT NULL DEFAULT '',
    message         TEXT    NOT NULL,
    payload         TEXT    NOT NULL DEFAULT ''
);
CREATE INDEX idx_events_time ON events (created_at DESC);
CREATE INDEX idx_events_kind ON events (kind, created_at DESC);
CREATE INDEX idx_events_trace ON events (trace_id);
