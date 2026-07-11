PRAGMA foreign_keys = ON;

CREATE TABLE IF NOT EXISTS asset (
    asset_key TEXT PRIMARY KEY,
    asset_type TEXT NOT NULL CHECK (asset_type IN (
        'module_profile', 'selector', 'obligation', 'oracle',
        'scenario', 'fault_point', 'schedule_template'
    )),
    name TEXT NOT NULL,
    module TEXT NOT NULL,
    selector TEXT,
    lifecycle_status TEXT NOT NULL DEFAULT 'candidate' CHECK (lifecycle_status IN (
        'candidate', 'validated', 'retired'
    )),
    trust_level TEXT NOT NULL DEFAULT 'hypothesis' CHECK (trust_level IN (
        'hypothesis', 'used', 'execution_verified', 'trusted', 'refuted'
    )),
    payload_json TEXT NOT NULL CHECK (json_valid(payload_json)),
    provenance_json TEXT NOT NULL CHECK (json_valid(provenance_json)),
    content_hash TEXT NOT NULL,
    created_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE INDEX IF NOT EXISTS idx_asset_lookup
    ON asset(module, selector, asset_type, lifecycle_status);

CREATE TABLE IF NOT EXISTS asset_revision (
    asset_key TEXT NOT NULL,
    content_hash TEXT NOT NULL,
    payload_json TEXT NOT NULL CHECK (json_valid(payload_json)),
    provenance_json TEXT NOT NULL CHECK (json_valid(provenance_json)),
    captured_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP,
    PRIMARY KEY (asset_key, content_hash),
    FOREIGN KEY (asset_key) REFERENCES asset(asset_key) ON DELETE CASCADE
);

CREATE TABLE IF NOT EXISTS asset_link (
    source_key TEXT NOT NULL,
    target_key TEXT NOT NULL,
    relation TEXT NOT NULL,
    rationale TEXT,
    created_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP,
    PRIMARY KEY (source_key, target_key, relation),
    FOREIGN KEY (source_key) REFERENCES asset(asset_key) ON DELETE CASCADE,
    FOREIGN KEY (target_key) REFERENCES asset(asset_key) ON DELETE CASCADE
);

CREATE INDEX IF NOT EXISTS idx_asset_link_target
    ON asset_link(target_key, relation);

CREATE TABLE IF NOT EXISTS run_result (
    run_key TEXT PRIMARY KEY,
    obligation_key TEXT NOT NULL,
    verdict TEXT NOT NULL CHECK (verdict IN ('RED', 'GREEN', 'INVALID', 'INFO')),
    code_ref_json TEXT NOT NULL CHECK (json_valid(code_ref_json)),
    environment_json TEXT NOT NULL CHECK (json_valid(environment_json)),
    evidence_json TEXT NOT NULL CHECK (json_valid(evidence_json)),
    lessons_json TEXT NOT NULL CHECK (json_valid(lessons_json)),
    created_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP,
    FOREIGN KEY (obligation_key) REFERENCES asset(asset_key)
);

CREATE INDEX IF NOT EXISTS idx_run_obligation
    ON run_result(obligation_key, verdict, created_at);

CREATE TABLE IF NOT EXISTS run_asset (
    run_key TEXT NOT NULL,
    asset_key TEXT NOT NULL,
    role TEXT NOT NULL,
    PRIMARY KEY (run_key, asset_key, role),
    FOREIGN KEY (run_key) REFERENCES run_result(run_key) ON DELETE CASCADE,
    FOREIGN KEY (asset_key) REFERENCES asset(asset_key)
);

CREATE TABLE IF NOT EXISTS target_queue (
    target_key TEXT PRIMARY KEY,
    title TEXT NOT NULL,
    module TEXT NOT NULL,
    selector TEXT NOT NULL,
    status TEXT NOT NULL DEFAULT 'candidate' CHECK (status IN (
        'candidate', 'ready', 'running', 'validated', 'blocked', 'retired'
    )),
    discoverability TEXT NOT NULL CHECK (discoverability IN (
        'SQL_ONLY', 'SOURCE_ONLY', 'FAULT_INJECTION',
        'CLUSTER_TOPOLOGY', 'STRESS_PERF', 'LOW_VALUE'
    )),
    obligation_class TEXT NOT NULL,
    priority INTEGER NOT NULL DEFAULT 0,
    consequence INTEGER NOT NULL DEFAULT 1,
    effort INTEGER NOT NULL DEFAULT 5,
    uncertainty INTEGER NOT NULL DEFAULT 5,
    payload_json TEXT NOT NULL CHECK (json_valid(payload_json)),
    provenance_json TEXT NOT NULL CHECK (json_valid(provenance_json)),
    created_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE INDEX IF NOT EXISTS idx_target_queue_next
    ON target_queue(status, priority DESC, consequence DESC, effort ASC);
