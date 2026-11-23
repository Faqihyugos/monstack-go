-- Migration 002: Alert system tables

CREATE TABLE IF NOT EXISTS alert_rules (
    id            TEXT PRIMARY KEY DEFAULT (lower(hex(randomblob(16)))),
    name          TEXT NOT NULL UNIQUE,
    expression    TEXT NOT NULL,
    for_duration  TEXT DEFAULT '0s',
    severity      TEXT NOT NULL DEFAULT 'warning' CHECK(severity IN ('info','warning','critical')),
    labels        TEXT DEFAULT '{}',
    annotations   TEXT DEFAULT '{}',
    target_match  TEXT DEFAULT '',
    channels      TEXT DEFAULT '[]',
    enabled       INTEGER DEFAULT 1,
    created_at    DATETIME DEFAULT CURRENT_TIMESTAMP,
    updated_at    DATETIME DEFAULT CURRENT_TIMESTAMP
);

CREATE TABLE IF NOT EXISTS alert_events (
    id                    INTEGER PRIMARY KEY AUTOINCREMENT,
    rule_id               TEXT NOT NULL REFERENCES alert_rules(id) ON DELETE CASCADE,
    rule_name             TEXT NOT NULL,
    target_name           TEXT,
    target_address        TEXT,
    status                TEXT NOT NULL CHECK(status IN ('firing','resolved','pending')),
    severity              TEXT NOT NULL,
    value                 REAL,
    message               TEXT,
    fired_at              DATETIME NOT NULL,
    resolved_at           DATETIME,
    notified_at           DATETIME,
    notification_channel  TEXT,
    notification_status   TEXT DEFAULT 'pending' CHECK(notification_status IN ('pending','sent','failed','skipped'))
);

CREATE TABLE IF NOT EXISTS alert_silences (
    id         TEXT PRIMARY KEY DEFAULT (lower(hex(randomblob(16)))),
    matchers   TEXT NOT NULL DEFAULT '{}',
    starts_at  DATETIME NOT NULL,
    ends_at    DATETIME NOT NULL,
    comment    TEXT,
    created_by TEXT,
    created_at DATETIME DEFAULT CURRENT_TIMESTAMP
);

CREATE INDEX IF NOT EXISTS idx_alert_events_rule ON alert_events(rule_id);
CREATE INDEX IF NOT EXISTS idx_alert_events_status ON alert_events(status);
CREATE INDEX IF NOT EXISTS idx_alert_events_fired ON alert_events(fired_at);
CREATE INDEX IF NOT EXISTS idx_alert_events_target ON alert_events(target_name);
CREATE INDEX IF NOT EXISTS idx_alert_silences_time ON alert_silences(starts_at, ends_at);
