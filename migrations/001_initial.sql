-- Migration 001: Initial schema
-- MonStack-Go metadata database

CREATE TABLE IF NOT EXISTS targets (
    id          TEXT PRIMARY KEY DEFAULT (lower(hex(randomblob(16)))),
    name        TEXT NOT NULL UNIQUE,
    type        TEXT NOT NULL CHECK(type IN ('http','icmp','tcp','udp','dns','snmp','grpc','tls','port_scan')),
    target      TEXT NOT NULL,
    interval_ms INTEGER NOT NULL DEFAULT 30000,
    timeout_ms  INTEGER NOT NULL DEFAULT 10000,
    retries     INTEGER NOT NULL DEFAULT 2,
    config      TEXT DEFAULT '{}',
    labels      TEXT DEFAULT '{}',
    enabled     INTEGER DEFAULT 1,
    created_at  DATETIME DEFAULT CURRENT_TIMESTAMP,
    updated_at  DATETIME DEFAULT CURRENT_TIMESTAMP
);

CREATE TABLE IF NOT EXISTS agents (
    id            TEXT PRIMARY KEY,
    hostname      TEXT NOT NULL,
    ip_address    TEXT NOT NULL,
    os            TEXT,
    arch          TEXT,
    version       TEXT,
    collectors    TEXT DEFAULT '[]',
    labels        TEXT DEFAULT '{}',
    last_seen_at  DATETIME,
    status        TEXT DEFAULT 'unknown' CHECK(status IN ('online','offline','unknown')),
    created_at    DATETIME DEFAULT CURRENT_TIMESTAMP
);

CREATE TABLE IF NOT EXISTS notification_channels (
    id      TEXT PRIMARY KEY DEFAULT (lower(hex(randomblob(16)))),
    name    TEXT NOT NULL UNIQUE,
    type    TEXT NOT NULL CHECK(type IN ('telegram','slack','webhook','email','pagerduty','discord')),
    config  TEXT NOT NULL DEFAULT '{}',
    enabled INTEGER DEFAULT 1,
    created_at DATETIME DEFAULT CURRENT_TIMESTAMP
);

CREATE TABLE IF NOT EXISTS users (
    id            TEXT PRIMARY KEY DEFAULT (lower(hex(randomblob(16)))),
    username      TEXT NOT NULL UNIQUE,
    password_hash TEXT NOT NULL,
    role          TEXT DEFAULT 'viewer' CHECK(role IN ('admin','editor','viewer')),
    api_key       TEXT UNIQUE,
    created_at    DATETIME DEFAULT CURRENT_TIMESTAMP,
    last_login    DATETIME
);

CREATE TABLE IF NOT EXISTS audit_log (
    id         INTEGER PRIMARY KEY AUTOINCREMENT,
    timestamp  DATETIME DEFAULT CURRENT_TIMESTAMP,
    actor      TEXT NOT NULL,
    action     TEXT NOT NULL,
    resource   TEXT NOT NULL,
    resource_id TEXT,
    details    TEXT,
    ip_address TEXT
);

CREATE INDEX IF NOT EXISTS idx_targets_type ON targets(type);
CREATE INDEX IF NOT EXISTS idx_targets_enabled ON targets(enabled);
CREATE INDEX IF NOT EXISTS idx_agents_status ON agents(status);
CREATE INDEX IF NOT EXISTS idx_agents_last_seen ON agents(last_seen_at);
CREATE INDEX IF NOT EXISTS idx_audit_timestamp ON audit_log(timestamp);
CREATE INDEX IF NOT EXISTS idx_audit_resource ON audit_log(resource, resource_id);
