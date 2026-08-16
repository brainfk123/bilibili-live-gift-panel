CREATE TABLE IF NOT EXISTS account_config_versions (
    id BIGINT UNSIGNED NOT NULL AUTO_INCREMENT,
    account_id BIGINT UNSIGNED NOT NULL,
    number BIGINT UNSIGNED NOT NULL,
    definition_json JSON NOT NULL,
    source VARCHAR(16) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
    created_at TIMESTAMP(6) NOT NULL DEFAULT CURRENT_TIMESTAMP(6),
    PRIMARY KEY (id),
    UNIQUE KEY uq_account_config_versions_account_number (account_id, number),
    UNIQUE KEY uq_account_config_versions_account_id (account_id, id),
    KEY idx_account_config_versions_account_created (account_id, created_at),
    CONSTRAINT fk_account_config_versions_account
        FOREIGN KEY (account_id) REFERENCES streamer_accounts (id)
        ON UPDATE RESTRICT ON DELETE RESTRICT,
    CONSTRAINT chk_account_config_versions_definition_json CHECK (JSON_VALID(definition_json)),
    CONSTRAINT chk_account_config_versions_source CHECK (source IN ('manual', 'migration', 'rollback'))
) ENGINE=InnoDB DEFAULT CHARACTER SET utf8mb4 COLLATE utf8mb4_0900_ai_ci;

CREATE TABLE IF NOT EXISTS account_active_config (
    account_id BIGINT UNSIGNED NOT NULL,
    config_version_id BIGINT UNSIGNED NOT NULL,
    updated_at TIMESTAMP(6) NOT NULL DEFAULT CURRENT_TIMESTAMP(6),
    PRIMARY KEY (account_id),
    KEY idx_account_active_config_account_version (account_id, config_version_id),
    CONSTRAINT fk_account_active_config_account
        FOREIGN KEY (account_id) REFERENCES streamer_accounts (id)
        ON UPDATE RESTRICT ON DELETE RESTRICT,
    CONSTRAINT fk_account_active_config_version
        FOREIGN KEY (account_id, config_version_id) REFERENCES account_config_versions (account_id, id)
        ON UPDATE RESTRICT ON DELETE RESTRICT
) ENGINE=InnoDB DEFAULT CHARACTER SET utf8mb4 COLLATE utf8mb4_0900_ai_ci;

CREATE TABLE IF NOT EXISTS account_runtime_state (
    account_id BIGINT UNSIGNED NOT NULL,
    config_version_id BIGINT UNSIGNED NOT NULL,
    revision BIGINT UNSIGNED NOT NULL,
    runtime_json JSON NOT NULL,
    updated_at TIMESTAMP(6) NOT NULL DEFAULT CURRENT_TIMESTAMP(6),
    PRIMARY KEY (account_id),
    KEY idx_account_runtime_state_account_version (account_id, config_version_id),
    CONSTRAINT fk_account_runtime_state_account
        FOREIGN KEY (account_id) REFERENCES streamer_accounts (id)
        ON UPDATE RESTRICT ON DELETE RESTRICT,
    CONSTRAINT fk_account_runtime_state_config_version
        FOREIGN KEY (account_id, config_version_id) REFERENCES account_config_versions (account_id, id)
        ON UPDATE RESTRICT ON DELETE RESTRICT,
    CONSTRAINT chk_account_runtime_state_revision CHECK (revision >= 1),
    CONSTRAINT chk_account_runtime_state_runtime_json CHECK (JSON_VALID(runtime_json))
) ENGINE=InnoDB DEFAULT CHARACTER SET utf8mb4 COLLATE utf8mb4_0900_ai_ci;

CREATE TABLE IF NOT EXISTS account_room_suggestions (
    account_id BIGINT UNSIGNED NOT NULL,
    room_id VARCHAR(128) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
    suggested_at TIMESTAMP(6) NOT NULL DEFAULT CURRENT_TIMESTAMP(6),
    PRIMARY KEY (account_id),
    CONSTRAINT fk_account_room_suggestions_account
        FOREIGN KEY (account_id) REFERENCES streamer_accounts (id)
        ON UPDATE RESTRICT ON DELETE RESTRICT,
    CONSTRAINT chk_account_room_suggestions_room_id CHECK (CHAR_LENGTH(room_id) BETWEEN 1 AND 128)
) ENGINE=InnoDB DEFAULT CHARACTER SET utf8mb4 COLLATE utf8mb4_0900_ai_ci;

CREATE TABLE IF NOT EXISTS migration_jobs (
    id BIGINT UNSIGNED NOT NULL AUTO_INCREMENT,
    account_id BIGINT UNSIGNED NOT NULL,
    request_hash BINARY(32) NOT NULL,
    status VARCHAR(16) CHARACTER SET ascii COLLATE ascii_bin NOT NULL DEFAULT 'previewed',
    active_request_hash BINARY(32) GENERATED ALWAYS AS (CASE WHEN status IN ('previewed', 'pending') THEN request_hash ELSE NULL END) STORED,
    base_config_version_number BIGINT UNSIGNED NOT NULL DEFAULT 0,
    base_state_revision BIGINT UNSIGNED NOT NULL DEFAULT 0,
    definition_json JSON NOT NULL,
    runtime_json JSON NOT NULL,
    room_suggestion VARCHAR(128) CHARACTER SET ascii COLLATE ascii_bin NULL,
    source_app_version VARCHAR(64) CHARACTER SET ascii COLLATE ascii_bin NULL,
    source_schema_version INT UNSIGNED NULL,
    report_json JSON NOT NULL,
    keep_room_suggestion TINYINT NOT NULL DEFAULT 0,
    rollback_config_version_id BIGINT UNSIGNED NULL,
    rollback_runtime_json JSON NULL,
    rollback_expires_at TIMESTAMP(6) NULL,
    applied_config_version_id BIGINT UNSIGNED NULL,
    created_at TIMESTAMP(6) NOT NULL DEFAULT CURRENT_TIMESTAMP(6),
    expires_at TIMESTAMP(6) NOT NULL,
    applied_at TIMESTAMP(6) NULL,
    cancelled_at TIMESTAMP(6) NULL,
    rolled_back_at TIMESTAMP(6) NULL,
    PRIMARY KEY (id),
    UNIQUE KEY uq_migration_jobs_account_active_hash (account_id, active_request_hash),
    KEY idx_migration_jobs_hash (account_id, request_hash),
    KEY idx_migration_jobs_status_expiry (status, expires_at),
    KEY idx_migration_jobs_account_created (account_id, created_at),
    KEY idx_migration_jobs_account_rollback_version (account_id, rollback_config_version_id),
    KEY idx_migration_jobs_rollback_expiry (rollback_expires_at),
    CONSTRAINT fk_migration_jobs_account
        FOREIGN KEY (account_id) REFERENCES streamer_accounts (id)
        ON UPDATE RESTRICT ON DELETE RESTRICT,
    CONSTRAINT fk_migration_jobs_rollback_config_version
        FOREIGN KEY (account_id, rollback_config_version_id) REFERENCES account_config_versions (account_id, id)
        ON UPDATE RESTRICT ON DELETE RESTRICT,
    CONSTRAINT fk_migration_jobs_applied_config_version
        FOREIGN KEY (account_id, applied_config_version_id) REFERENCES account_config_versions (account_id, id)
        ON UPDATE RESTRICT ON DELETE RESTRICT,
    CONSTRAINT chk_migration_jobs_definition_json CHECK (JSON_VALID(definition_json)),
    CONSTRAINT chk_migration_jobs_runtime_json CHECK (JSON_VALID(runtime_json)),
    CONSTRAINT chk_migration_jobs_report_json CHECK (JSON_VALID(report_json)),
    CONSTRAINT chk_migration_jobs_rollback_runtime_json CHECK (rollback_runtime_json IS NULL OR JSON_VALID(rollback_runtime_json)),
    CONSTRAINT chk_migration_jobs_keep_room_suggestion CHECK (keep_room_suggestion IN (0, 1)),
    CONSTRAINT chk_migration_jobs_status CHECK (status IN ('previewed', 'pending', 'applied', 'cancelled', 'rolled_back', 'expired')),
    CONSTRAINT chk_migration_jobs_expiry CHECK (expires_at > created_at)
) ENGINE=InnoDB DEFAULT CHARACTER SET utf8mb4 COLLATE utf8mb4_0900_ai_ci;

CREATE TABLE IF NOT EXISTS live_sessions (
    id BIGINT UNSIGNED NOT NULL AUTO_INCREMENT,
    account_id BIGINT UNSIGNED NOT NULL,
    room_id VARCHAR(128) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
    config_version_id BIGINT UNSIGNED NOT NULL,
    started_at TIMESTAMP(6) NOT NULL DEFAULT CURRENT_TIMESTAMP(6),
    ended_at TIMESTAMP(6) NULL,
    PRIMARY KEY (id),
    KEY idx_live_sessions_account_started (account_id, started_at),
    KEY idx_live_sessions_account_opened (account_id, ended_at),
    KEY idx_live_sessions_account_version (account_id, config_version_id),
    CONSTRAINT fk_live_sessions_account
        FOREIGN KEY (account_id) REFERENCES streamer_accounts (id)
        ON UPDATE RESTRICT ON DELETE RESTRICT,
    CONSTRAINT fk_live_sessions_config_version
        FOREIGN KEY (account_id, config_version_id) REFERENCES account_config_versions (account_id, id)
        ON UPDATE RESTRICT ON DELETE RESTRICT,
    CONSTRAINT chk_live_sessions_room_id CHECK (CHAR_LENGTH(room_id) BETWEEN 1 AND 128),
    CONSTRAINT chk_live_sessions_end_after_start CHECK (ended_at IS NULL OR ended_at >= started_at)
) ENGINE=InnoDB DEFAULT CHARACTER SET utf8mb4 COLLATE utf8mb4_0900_ai_ci;
