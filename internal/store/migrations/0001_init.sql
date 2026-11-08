-- 0001_init.sql
CREATE TABLE IF NOT EXISTS users (
    id            TEXT PRIMARY KEY,
    username      TEXT NOT NULL UNIQUE,
    password_hash TEXT NOT NULL,
    created_at    TEXT NOT NULL,
    updated_at    TEXT NOT NULL
);

CREATE TABLE IF NOT EXISTS sessions (
    id         TEXT PRIMARY KEY,
    user_id    TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    token_hash TEXT NOT NULL UNIQUE,
    csrf_token TEXT NOT NULL,
    ip         TEXT NOT NULL DEFAULT '',
    user_agent TEXT NOT NULL DEFAULT '',
    created_at TEXT NOT NULL,
    last_seen_at TEXT NOT NULL,
    expires_at TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_sessions_user ON sessions(user_id);
CREATE INDEX IF NOT EXISTS idx_sessions_expiry ON sessions(expires_at);

CREATE TABLE IF NOT EXISTS credentials (
    id                   TEXT PRIMARY KEY,
    kind                 TEXT NOT NULL,
    ciphertext           TEXT NOT NULL,
    passphrase_ciphertext TEXT NOT NULL DEFAULT '',
    key_version          INTEGER NOT NULL DEFAULT 1,
    comment              TEXT NOT NULL DEFAULT '',
    created_at           TEXT NOT NULL
);

CREATE TABLE IF NOT EXISTS hosts (
    id                  TEXT PRIMARY KEY,
    name                TEXT NOT NULL UNIQUE,
    address             TEXT NOT NULL,
    ssh_port            INTEGER NOT NULL DEFAULT 22,
    ssh_user            TEXT NOT NULL,
    auth_type           TEXT NOT NULL,
    credential_id       TEXT NOT NULL REFERENCES credentials(id),
    host_key_fingerprint TEXT NOT NULL DEFAULT '',
    sudo_mode           TEXT NOT NULL DEFAULT 'none',
    sudo_credential_id  TEXT NOT NULL DEFAULT '',
    status              TEXT NOT NULL DEFAULT 'unknown',
    status_message      TEXT NOT NULL DEFAULT '',
    last_probe_at       TEXT,
    created_at          TEXT NOT NULL,
    updated_at          TEXT NOT NULL
);

CREATE TABLE IF NOT EXISTS host_capabilities (
    host_id                  TEXT PRIMARY KEY REFERENCES hosts(id) ON DELETE CASCADE,
    os_id                    TEXT NOT NULL DEFAULT '',
    os_version               TEXT NOT NULL DEFAULT '',
    os_codename              TEXT NOT NULL DEFAULT '',
    os_pretty                TEXT NOT NULL DEFAULT '',
    arch                     TEXT NOT NULL DEFAULT '',
    kernel                   TEXT NOT NULL DEFAULT '',
    init_system              TEXT NOT NULL DEFAULT '',
    cgroup_version           TEXT NOT NULL DEFAULT '',
    cpu_count                INTEGER NOT NULL DEFAULT 0,
    mem_mb                   INTEGER NOT NULL DEFAULT 0,
    disk_free_kb             INTEGER NOT NULL DEFAULT 0,
    docker_present           INTEGER NOT NULL DEFAULT 0,
    docker_client_version    TEXT NOT NULL DEFAULT '',
    docker_server_version    TEXT NOT NULL DEFAULT '',
    docker_running           INTEGER NOT NULL DEFAULT 0,
    docker_socket_access     TEXT NOT NULL DEFAULT 'absent',
    docker_sudo              INTEGER NOT NULL DEFAULT 0,
    docker_rootless          INTEGER NOT NULL DEFAULT 0,
    compose_kind             TEXT NOT NULL DEFAULT 'none',
    compose_version          TEXT NOT NULL DEFAULT '',
    compose_major            INTEGER NOT NULL DEFAULT 0,
    compose_path             TEXT NOT NULL DEFAULT '',
    compose_v1_binary_present INTEGER NOT NULL DEFAULT 0,
    ports_in_use             TEXT NOT NULL DEFAULT '[]',
    unsupported_reason       TEXT NOT NULL DEFAULT '',
    raw_json                 TEXT NOT NULL DEFAULT '{}',
    probed_at                TEXT NOT NULL
);

CREATE TABLE IF NOT EXISTS instances (
    id                 TEXT PRIMARY KEY,
    name               TEXT NOT NULL,
    slug               TEXT NOT NULL,
    host_id            TEXT NOT NULL REFERENCES hosts(id),
    project_name       TEXT NOT NULL,
    base_dir           TEXT NOT NULL,
    bind_address       TEXT NOT NULL DEFAULT '0.0.0.0',
    tm_port            INTEGER NOT NULL,
    grafana_port       INTEGER NOT NULL,
    mqtt_port          INTEGER NOT NULL,
    timezone           TEXT NOT NULL DEFAULT 'UTC',
    region             TEXT NOT NULL DEFAULT 'global',
    image_tag          TEXT NOT NULL DEFAULT 'latest',
    postgres_tag       TEXT NOT NULL DEFAULT '18-trixie',
    grafana_admin_user TEXT NOT NULL DEFAULT 'admin',
    disable_mqtt       INTEGER NOT NULL DEFAULT 0,
    extra_env          TEXT NOT NULL DEFAULT '{}',
    tm_encryption_key  TEXT NOT NULL,
    db_user            TEXT NOT NULL DEFAULT 'teslamate',
    db_pass            TEXT NOT NULL,
    grafana_pass       TEXT NOT NULL,
    compose_major      INTEGER NOT NULL DEFAULT 2,
    compose_sha256     TEXT NOT NULL DEFAULT '',
    status             TEXT NOT NULL DEFAULT 'created',
    status_message     TEXT NOT NULL DEFAULT '',
    last_job_id        TEXT NOT NULL DEFAULT '',
    created_at         TEXT NOT NULL,
    updated_at         TEXT NOT NULL,
    UNIQUE(host_id, slug),
    UNIQUE(host_id, tm_port),
    UNIQUE(host_id, grafana_port),
    UNIQUE(host_id, mqtt_port)
);
CREATE INDEX IF NOT EXISTS idx_instances_host ON instances(host_id);

CREATE TABLE IF NOT EXISTS jobs (
    id          TEXT PRIMARY KEY,
    kind        TEXT NOT NULL,
    host_id     TEXT NOT NULL DEFAULT '',
    instance_id TEXT NOT NULL DEFAULT '',
    status      TEXT NOT NULL DEFAULT 'queued',
    stage       TEXT NOT NULL DEFAULT '',
    exit_code   INTEGER NOT NULL DEFAULT 0,
    error       TEXT NOT NULL DEFAULT '',
    payload     TEXT NOT NULL DEFAULT '{}',
    log_path    TEXT NOT NULL DEFAULT '',
    created_by  TEXT NOT NULL DEFAULT '',
    created_at  TEXT NOT NULL,
    started_at  TEXT,
    finished_at TEXT
);
CREATE INDEX IF NOT EXISTS idx_jobs_status ON jobs(status);
CREATE INDEX IF NOT EXISTS idx_jobs_instance ON jobs(instance_id);
CREATE INDEX IF NOT EXISTS idx_jobs_created ON jobs(created_at);

CREATE TABLE IF NOT EXISTS backups (
    id          TEXT PRIMARY KEY,
    instance_id TEXT NOT NULL REFERENCES instances(id) ON DELETE CASCADE,
    filename    TEXT NOT NULL,
    size_bytes  INTEGER NOT NULL DEFAULT 0,
    sha256      TEXT NOT NULL DEFAULT '',
    format      TEXT NOT NULL DEFAULT 'fc',
    note        TEXT NOT NULL DEFAULT '',
    created_at  TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_backups_instance ON backups(instance_id);

CREATE TABLE IF NOT EXISTS audit_logs (
    id          TEXT PRIMARY KEY,
    actor       TEXT NOT NULL DEFAULT '',
    action      TEXT NOT NULL,
    target_type TEXT NOT NULL DEFAULT '',
    target_id   TEXT NOT NULL DEFAULT '',
    detail_json TEXT NOT NULL DEFAULT '{}',
    ip          TEXT NOT NULL DEFAULT '',
    created_at  TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_audit_created ON audit_logs(created_at);

CREATE TABLE IF NOT EXISTS settings (
    key   TEXT PRIMARY KEY,
    value TEXT NOT NULL
);
