CREATE TABLE studios (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    slug TEXT NOT NULL UNIQUE,
    name TEXT NOT NULL,
    daily_render_capacity INTEGER NOT NULL CHECK (daily_render_capacity > 0),
    created_at TEXT NOT NULL
);

CREATE TABLE users (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    studio_id INTEGER NOT NULL REFERENCES studios (id) ON DELETE RESTRICT,
    email TEXT NOT NULL,
    display_name TEXT NOT NULL,
    password_hash TEXT NOT NULL,
    role TEXT NOT NULL CHECK (role IN ('director', 'apprentice')),
    status TEXT NOT NULL CHECK (status IN ('active', 'suspended')),
    created_at TEXT NOT NULL,
    UNIQUE (studio_id, email)
);

CREATE TABLE sessions (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    user_id INTEGER NOT NULL REFERENCES users (id) ON DELETE CASCADE,
    studio_id INTEGER NOT NULL REFERENCES studios (id) ON DELETE RESTRICT,
    token_hash TEXT NOT NULL UNIQUE,
    generation INTEGER NOT NULL DEFAULT 1,
    issued_at TEXT NOT NULL,
    expires_at TEXT NOT NULL,
    revoked_at TEXT,
    last_seen_at TEXT NOT NULL
);

CREATE INDEX idx_sessions_user_active ON sessions (user_id, revoked_at, expires_at);

CREATE TABLE prompt_templates (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    studio_id INTEGER NOT NULL REFERENCES studios (id) ON DELETE RESTRICT,
    owner_id INTEGER NOT NULL REFERENCES users (id) ON DELETE RESTRICT,
    slug TEXT NOT NULL,
    title TEXT NOT NULL,
    discipline TEXT NOT NULL DEFAULT '',
    head_version INTEGER NOT NULL DEFAULT 0,
    created_at TEXT NOT NULL,
    updated_at TEXT NOT NULL,
    UNIQUE (studio_id, slug)
);

CREATE TABLE prompt_versions (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    template_id INTEGER NOT NULL REFERENCES prompt_templates (id) ON DELETE RESTRICT,
    version INTEGER NOT NULL CHECK (version > 0),
    body TEXT NOT NULL,
    checksum TEXT NOT NULL,
    status TEXT NOT NULL CHECK (status IN ('draft', 'active', 'retired')),
    notes TEXT NOT NULL DEFAULT '',
    created_by INTEGER NOT NULL REFERENCES users (id) ON DELETE RESTRICT,
    created_at TEXT NOT NULL,
    retired_at TEXT,
    UNIQUE (template_id, version)
);

CREATE INDEX idx_prompt_versions_status ON prompt_versions (template_id, status);

CREATE TABLE series (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    studio_id INTEGER NOT NULL REFERENCES studios (id) ON DELETE RESTRICT,
    code TEXT NOT NULL,
    title TEXT NOT NULL,
    logline TEXT NOT NULL DEFAULT '',
    state TEXT NOT NULL CHECK (state IN ('draft', 'shooting', 'reviewing', 'published', 'archived', 'cancelled')),
    version INTEGER NOT NULL DEFAULT 1,
    created_by INTEGER NOT NULL REFERENCES users (id) ON DELETE RESTRICT,
    created_at TEXT NOT NULL,
    updated_at TEXT NOT NULL,
    published_at TEXT,
    UNIQUE (studio_id, code)
);

CREATE INDEX idx_series_state ON series (studio_id, state, created_at);

CREATE TABLE shots (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    series_id INTEGER NOT NULL REFERENCES series (id) ON DELETE CASCADE,
    ordinal INTEGER NOT NULL CHECK (ordinal > 0),
    title TEXT NOT NULL,
    direction TEXT NOT NULL DEFAULT '',
    state TEXT NOT NULL CHECK (state IN ('draft', 'bound', 'rendering', 'rendered', 'approved', 'rework')),
    prompt_version_id INTEGER REFERENCES prompt_versions (id) ON DELETE RESTRICT,
    artifact_ref TEXT NOT NULL DEFAULT '',
    rework_reason TEXT NOT NULL DEFAULT '',
    version INTEGER NOT NULL DEFAULT 1,
    created_at TEXT NOT NULL,
    updated_at TEXT NOT NULL,
    UNIQUE (series_id, ordinal)
);

CREATE INDEX idx_shots_state ON shots (series_id, state);
CREATE INDEX idx_shots_prompt_version ON shots (prompt_version_id);

CREATE TABLE render_quotas (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    studio_id INTEGER NOT NULL REFERENCES studios (id) ON DELETE RESTRICT,
    quota_day TEXT NOT NULL,
    capacity INTEGER NOT NULL CHECK (capacity > 0),
    used INTEGER NOT NULL DEFAULT 0 CHECK (used >= 0),
    updated_at TEXT NOT NULL,
    UNIQUE (studio_id, quota_day)
);

CREATE TABLE render_jobs (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    studio_id INTEGER NOT NULL REFERENCES studios (id) ON DELETE RESTRICT,
    series_id INTEGER NOT NULL REFERENCES series (id) ON DELETE CASCADE,
    shot_id INTEGER NOT NULL REFERENCES shots (id) ON DELETE CASCADE,
    prompt_version_id INTEGER NOT NULL REFERENCES prompt_versions (id) ON DELETE RESTRICT,
    quota_day TEXT NOT NULL,
    state TEXT NOT NULL CHECK (state IN ('queued', 'leased', 'retrying', 'succeeded', 'failed_permanent', 'cancelled')),
    attempts INTEGER NOT NULL DEFAULT 0,
    max_attempts INTEGER NOT NULL CHECK (max_attempts > 0),
    next_attempt_at TEXT NOT NULL,
    lease_owner TEXT NOT NULL DEFAULT '',
    lease_expires_at TEXT,
    lease_generation INTEGER NOT NULL DEFAULT 0,
    last_error TEXT NOT NULL DEFAULT '',
    artifact_ref TEXT NOT NULL DEFAULT '',
    requested_by INTEGER NOT NULL REFERENCES users (id) ON DELETE RESTRICT,
    created_at TEXT NOT NULL,
    updated_at TEXT NOT NULL,
    finished_at TEXT
);

CREATE INDEX idx_render_jobs_due ON render_jobs (state, next_attempt_at);
CREATE INDEX idx_render_jobs_prompt_version ON render_jobs (prompt_version_id, state);
CREATE UNIQUE INDEX idx_render_jobs_active_shot ON render_jobs (shot_id)
    WHERE state IN ('queued', 'leased', 'retrying');

CREATE TABLE workshops (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    studio_id INTEGER NOT NULL REFERENCES studios (id) ON DELETE RESTRICT,
    series_id INTEGER NOT NULL REFERENCES series (id) ON DELETE RESTRICT,
    shot_id INTEGER NOT NULL REFERENCES shots (id) ON DELETE RESTRICT,
    prompt_version_id INTEGER NOT NULL REFERENCES prompt_versions (id) ON DELETE RESTRICT,
    mentor_id INTEGER NOT NULL REFERENCES users (id) ON DELETE RESTRICT,
    title TEXT NOT NULL,
    brief TEXT NOT NULL DEFAULT '',
    state TEXT NOT NULL CHECK (state IN ('open', 'teaching', 'grading', 'closed', 'cancelled')),
    capacity INTEGER NOT NULL CHECK (capacity > 0),
    enrolled INTEGER NOT NULL DEFAULT 0 CHECK (enrolled >= 0),
    opens_at TEXT NOT NULL,
    closes_at TEXT NOT NULL,
    version INTEGER NOT NULL DEFAULT 1,
    created_at TEXT NOT NULL,
    updated_at TEXT NOT NULL
);

CREATE INDEX idx_workshops_state ON workshops (studio_id, state, closes_at);
CREATE INDEX idx_workshops_prompt_version ON workshops (prompt_version_id, state);

CREATE TABLE enrollments (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    workshop_id INTEGER NOT NULL REFERENCES workshops (id) ON DELETE CASCADE,
    apprentice_id INTEGER NOT NULL REFERENCES users (id) ON DELETE RESTRICT,
    state TEXT NOT NULL CHECK (state IN ('enrolled', 'submitted', 'graded')),
    created_at TEXT NOT NULL,
    updated_at TEXT NOT NULL,
    UNIQUE (workshop_id, apprentice_id)
);

CREATE TABLE practice_submissions (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    workshop_id INTEGER NOT NULL REFERENCES workshops (id) ON DELETE CASCADE,
    enrollment_id INTEGER NOT NULL REFERENCES enrollments (id) ON DELETE CASCADE,
    prompt_version_id INTEGER NOT NULL REFERENCES prompt_versions (id) ON DELETE RESTRICT,
    body TEXT NOT NULL,
    state TEXT NOT NULL CHECK (state IN ('pending', 'accepted', 'returned')),
    score INTEGER NOT NULL DEFAULT 0 CHECK (score >= 0 AND score <= 100),
    feedback TEXT NOT NULL DEFAULT '',
    reviewed_by INTEGER REFERENCES users (id) ON DELETE RESTRICT,
    reviewed_at TEXT,
    version INTEGER NOT NULL DEFAULT 1,
    created_at TEXT NOT NULL,
    updated_at TEXT NOT NULL
);

CREATE INDEX idx_submissions_state ON practice_submissions (workshop_id, state);

CREATE TABLE audit_events (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    studio_id INTEGER NOT NULL REFERENCES studios (id) ON DELETE RESTRICT,
    actor_id INTEGER NOT NULL,
    actor_role TEXT NOT NULL,
    action TEXT NOT NULL,
    object_type TEXT NOT NULL,
    object_id INTEGER NOT NULL,
    result TEXT NOT NULL CHECK (result IN ('success', 'rejected')),
    request_id TEXT NOT NULL DEFAULT '',
    detail TEXT NOT NULL DEFAULT '{}',
    created_at TEXT NOT NULL
);

CREATE INDEX idx_audit_object ON audit_events (studio_id, object_type, object_id, id);

CREATE TABLE idempotency_records (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    studio_id INTEGER NOT NULL REFERENCES studios (id) ON DELETE RESTRICT,
    method TEXT NOT NULL,
    path TEXT NOT NULL,
    key TEXT NOT NULL,
    fingerprint TEXT NOT NULL,
    state TEXT NOT NULL CHECK (state IN ('in_progress', 'completed', 'failed')),
    status_code INTEGER NOT NULL DEFAULT 0,
    response TEXT NOT NULL DEFAULT '',
    created_at TEXT NOT NULL,
    completed_at TEXT,
    UNIQUE (studio_id, method, path, key)
);

CREATE TABLE business_sequences (
    studio_id INTEGER NOT NULL REFERENCES studios (id) ON DELETE RESTRICT,
    name TEXT NOT NULL,
    value INTEGER NOT NULL DEFAULT 0,
    PRIMARY KEY (studio_id, name)
);
