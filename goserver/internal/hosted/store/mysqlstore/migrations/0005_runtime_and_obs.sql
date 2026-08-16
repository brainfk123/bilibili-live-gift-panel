-- Runtime and OBS storage extends the configuration/migration schema. In
-- particular, 0004's live_sessions table is never rebuilt here.

CREATE TABLE IF NOT EXISTS bili_service_credentials (
    id BIGINT UNSIGNED NOT NULL AUTO_INCREMENT,
    credential_version BIGINT UNSIGNED NOT NULL,
    cookie_ciphertext VARBINARY(4096) NOT NULL,
    created_at TIMESTAMP(6) NOT NULL DEFAULT CURRENT_TIMESTAMP(6),
    revoked_at TIMESTAMP(6) NULL,
    active_guard TINYINT UNSIGNED GENERATED ALWAYS AS (CASE WHEN revoked_at IS NULL THEN 1 ELSE NULL END) STORED,
    PRIMARY KEY (id),
    UNIQUE KEY uq_bili_service_credentials_version (credential_version),
    UNIQUE KEY uq_bili_service_credentials_active (active_guard),
    KEY idx_bili_service_credentials_active (revoked_at, credential_version),
    CONSTRAINT chk_bili_service_credentials_version CHECK (credential_version >= 1)
) ENGINE=InnoDB DEFAULT CHARACTER SET utf8mb4 COLLATE utf8mb4_0900_ai_ci;

CREATE TABLE IF NOT EXISTS account_runtime_rooms (
    account_id BIGINT UNSIGNED NOT NULL,
    room_id VARCHAR(20) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
    updated_at TIMESTAMP(6) NOT NULL DEFAULT CURRENT_TIMESTAMP(6) ON UPDATE CURRENT_TIMESTAMP(6),
    PRIMARY KEY (account_id),
    CONSTRAINT fk_account_runtime_rooms_account FOREIGN KEY (account_id) REFERENCES streamer_accounts (id)
        ON UPDATE RESTRICT ON DELETE RESTRICT,
    CONSTRAINT chk_account_runtime_rooms_room_id CHECK (room_id REGEXP '^[1-9][0-9]{0,19}$')
) ENGINE=InnoDB DEFAULT CHARACTER SET utf8mb4 COLLATE utf8mb4_0900_ai_ci;

CREATE TABLE IF NOT EXISTS live_session_runtime (
    live_session_id BIGINT UNSIGNED NOT NULL,
    session_status VARCHAR(16) CHARACTER SET ascii COLLATE ascii_bin NOT NULL DEFAULT 'active',
    config_revision BIGINT UNSIGNED NOT NULL DEFAULT 1,
    updated_at TIMESTAMP(6) NOT NULL DEFAULT CURRENT_TIMESTAMP(6) ON UPDATE CURRENT_TIMESTAMP(6),
    PRIMARY KEY (live_session_id),
    CONSTRAINT fk_live_session_runtime_session FOREIGN KEY (live_session_id) REFERENCES live_sessions (id)
        ON UPDATE RESTRICT ON DELETE RESTRICT,
    CONSTRAINT chk_live_session_runtime_status CHECK (session_status IN ('active', 'ended', 'degraded')),
    CONSTRAINT chk_live_session_runtime_config_revision CHECK (config_revision >= 1)
) ENGINE=InnoDB DEFAULT CHARACTER SET utf8mb4 COLLATE utf8mb4_0900_ai_ci;

CREATE TABLE IF NOT EXISTS live_session_aggregates (
    live_session_id BIGINT UNSIGNED NOT NULL,
    aggregate_json JSON NOT NULL,
    updated_at TIMESTAMP(6) NOT NULL DEFAULT CURRENT_TIMESTAMP(6) ON UPDATE CURRENT_TIMESTAMP(6),
    PRIMARY KEY (live_session_id),
    CONSTRAINT fk_live_session_aggregates_session FOREIGN KEY (live_session_id) REFERENCES live_sessions (id)
        ON UPDATE RESTRICT ON DELETE RESTRICT,
    CONSTRAINT chk_live_session_aggregates_json CHECK (JSON_VALID(aggregate_json))
) ENGINE=InnoDB DEFAULT CHARACTER SET utf8mb4 COLLATE utf8mb4_0900_ai_ci;

CREATE TABLE IF NOT EXISTS live_session_event_dedupes (
    account_id BIGINT UNSIGNED NOT NULL,
    live_session_id BIGINT UNSIGNED NULL,
    event_hash BINARY(32) NOT NULL,
    created_at TIMESTAMP(6) NOT NULL DEFAULT CURRENT_TIMESTAMP(6),
    expires_at TIMESTAMP(6) NOT NULL,
    PRIMARY KEY (account_id, event_hash),
    KEY idx_live_session_event_dedupes_expiry (expires_at),
    KEY idx_live_session_event_dedupes_session (live_session_id),
    CONSTRAINT fk_live_session_event_dedupes_account FOREIGN KEY (account_id) REFERENCES streamer_accounts (id)
        ON UPDATE RESTRICT ON DELETE RESTRICT,
    CONSTRAINT fk_live_session_event_dedupes_session FOREIGN KEY (live_session_id) REFERENCES live_sessions (id)
        ON UPDATE RESTRICT ON DELETE SET NULL,
    CONSTRAINT chk_live_session_event_dedupes_expiry CHECK (expires_at > created_at)
) ENGINE=InnoDB DEFAULT CHARACTER SET utf8mb4 COLLATE utf8mb4_0900_ai_ci;

CREATE TABLE IF NOT EXISTS obs_credentials (
    id BIGINT UNSIGNED NOT NULL AUTO_INCREMENT,
    account_id BIGINT UNSIGNED NOT NULL,
    public_id CHAR(43) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
    token_hash BINARY(32) NOT NULL,
    credential_epoch BIGINT UNSIGNED NOT NULL,
    created_at TIMESTAMP(6) NOT NULL DEFAULT CURRENT_TIMESTAMP(6),
    revoked_at TIMESTAMP(6) NULL,
    active_account_id BIGINT UNSIGNED GENERATED ALWAYS AS (CASE WHEN revoked_at IS NULL THEN account_id ELSE NULL END) STORED,
    PRIMARY KEY (id),
    UNIQUE KEY uq_obs_credentials_public_id (public_id),
    UNIQUE KEY uq_obs_credentials_token_hash (token_hash),
    UNIQUE KEY uq_obs_credentials_active_account (active_account_id),
    KEY idx_obs_credentials_account (account_id, revoked_at),
    CONSTRAINT fk_obs_credentials_account FOREIGN KEY (account_id) REFERENCES streamer_accounts (id)
        ON UPDATE RESTRICT ON DELETE RESTRICT,
    CONSTRAINT chk_obs_credentials_epoch CHECK (credential_epoch >= 1)
) ENGINE=InnoDB DEFAULT CHARACTER SET utf8mb4 COLLATE utf8mb4_0900_ai_ci;

CREATE TABLE IF NOT EXISTS obs_sessions (
    id BIGINT UNSIGNED NOT NULL AUTO_INCREMENT,
    obs_credential_id BIGINT UNSIGNED NOT NULL,
    token_hash BINARY(32) NOT NULL,
    credential_epoch BIGINT UNSIGNED NOT NULL,
    created_at TIMESTAMP(6) NOT NULL DEFAULT CURRENT_TIMESTAMP(6),
    expires_at TIMESTAMP(6) NOT NULL,
    revoked_at TIMESTAMP(6) NULL,
    PRIMARY KEY (id),
    UNIQUE KEY uq_obs_sessions_token_hash (token_hash),
    KEY idx_obs_sessions_credential (obs_credential_id, expires_at),
    CONSTRAINT fk_obs_sessions_credential FOREIGN KEY (obs_credential_id) REFERENCES obs_credentials (id)
        ON UPDATE RESTRICT ON DELETE RESTRICT,
    CONSTRAINT chk_obs_sessions_epoch CHECK (credential_epoch >= 1),
    CONSTRAINT chk_obs_sessions_expiry CHECK (expires_at > created_at)
) ENGINE=InnoDB DEFAULT CHARACTER SET utf8mb4 COLLATE utf8mb4_0900_ai_ci;
