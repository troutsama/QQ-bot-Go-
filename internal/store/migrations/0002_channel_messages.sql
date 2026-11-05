-- 独立渠道通信记录；可为已有数据库补建，不改动问答业务表。
CREATE TABLE IF NOT EXISTS channel_messages (
    id                INTEGER PRIMARY KEY AUTOINCREMENT,
    provider          TEXT NOT NULL,
    app_id            TEXT NOT NULL,
    scene             TEXT NOT NULL,
    scene_id          TEXT NOT NULL,
    direction         TEXT NOT NULL CHECK (direction IN ('incoming', 'outgoing')),
    msg_id            TEXT NOT NULL,
    reply_to_msg_id   TEXT NOT NULL DEFAULT '',
    reply_to_event_id TEXT NOT NULL DEFAULT '',
    reply_seq         INTEGER NOT NULL DEFAULT 0,
    sender_openid     TEXT NOT NULL DEFAULT '',
    kind              TEXT NOT NULL,
    content           TEXT NOT NULL DEFAULT '',
    has_attachment    INTEGER NOT NULL DEFAULT 0,
    occurred_at       INTEGER NOT NULL,
    recorded_at       INTEGER NOT NULL,
    UNIQUE (provider, app_id, scene, scene_id, direction, msg_id)
);
CREATE INDEX IF NOT EXISTS idx_channel_messages_conversation
    ON channel_messages (provider, app_id, scene, scene_id, occurred_at, id);
