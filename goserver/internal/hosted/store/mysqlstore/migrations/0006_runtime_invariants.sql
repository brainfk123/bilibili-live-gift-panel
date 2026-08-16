-- live_sessions remains the only session lifecycle source. These companion
-- tables add immutable tenant identity, active ownership, and projections.

CREATE TABLE IF NOT EXISTS runtime_session_identities (
    live_session_id BIGINT UNSIGNED NOT NULL,
    account_id BIGINT UNSIGNED NOT NULL,
    created_at TIMESTAMP(6) NOT NULL DEFAULT CURRENT_TIMESTAMP(6),
    PRIMARY KEY (live_session_id),
    UNIQUE KEY uq_runtime_session_identities_session_account (live_session_id, account_id),
    KEY idx_runtime_session_identities_account (account_id, live_session_id),
    CONSTRAINT fk_runtime_session_identities_session
        FOREIGN KEY (live_session_id) REFERENCES live_sessions (id)
        ON UPDATE RESTRICT ON DELETE RESTRICT,
    CONSTRAINT fk_runtime_session_identities_account
        FOREIGN KEY (account_id) REFERENCES streamer_accounts (id)
        ON UPDATE RESTRICT ON DELETE RESTRICT
) ENGINE=InnoDB DEFAULT CHARACTER SET utf8mb4 COLLATE utf8mb4_0900_ai_ci;

CREATE TABLE IF NOT EXISTS runtime_active_session_guards (
    account_id BIGINT UNSIGNED NOT NULL,
    live_session_id BIGINT UNSIGNED NOT NULL,
    created_at TIMESTAMP(6) NOT NULL DEFAULT CURRENT_TIMESTAMP(6),
    PRIMARY KEY (account_id),
    UNIQUE KEY uq_runtime_active_session_guards_session (live_session_id),
    KEY idx_runtime_active_session_guards_session_account (live_session_id, account_id),
    CONSTRAINT fk_runtime_active_session_guards_identity
        FOREIGN KEY (live_session_id, account_id) REFERENCES runtime_session_identities (live_session_id, account_id)
        ON UPDATE RESTRICT ON DELETE RESTRICT
) ENGINE=InnoDB DEFAULT CHARACTER SET utf8mb4 COLLATE utf8mb4_0900_ai_ci;

CREATE TABLE IF NOT EXISTS runtime_session_aggregates (
    live_session_id BIGINT UNSIGNED NOT NULL,
    account_id BIGINT UNSIGNED NOT NULL,
    aggregate_json JSON NOT NULL,
    updated_at TIMESTAMP(6) NOT NULL DEFAULT CURRENT_TIMESTAMP(6) ON UPDATE CURRENT_TIMESTAMP(6),
    PRIMARY KEY (live_session_id),
    KEY idx_runtime_session_aggregates_session_account (live_session_id, account_id),
    KEY idx_runtime_session_aggregates_account (account_id, live_session_id),
    CONSTRAINT fk_runtime_session_aggregates_identity
        FOREIGN KEY (live_session_id, account_id) REFERENCES runtime_session_identities (live_session_id, account_id)
        ON UPDATE RESTRICT ON DELETE RESTRICT,
    CONSTRAINT chk_runtime_session_aggregates_json CHECK (JSON_VALID(aggregate_json))
) ENGINE=InnoDB DEFAULT CHARACTER SET utf8mb4 COLLATE utf8mb4_0900_ai_ci;

CREATE TABLE IF NOT EXISTS runtime_event_dedup_receipts (
    account_id BIGINT UNSIGNED NOT NULL,
    live_session_id BIGINT UNSIGNED NOT NULL,
    event_hash BINARY(32) NOT NULL,
    created_at TIMESTAMP(6) NOT NULL DEFAULT CURRENT_TIMESTAMP(6),
    expires_at TIMESTAMP(6) NOT NULL,
    PRIMARY KEY (account_id, event_hash),
    KEY idx_runtime_event_dedup_receipts_expiry (expires_at),
    KEY idx_runtime_event_dedup_receipts_session_account (live_session_id, account_id),
    CONSTRAINT fk_runtime_event_dedup_receipts_account
        FOREIGN KEY (account_id) REFERENCES streamer_accounts (id)
        ON UPDATE RESTRICT ON DELETE RESTRICT,
    CONSTRAINT fk_runtime_event_dedup_receipts_identity
        FOREIGN KEY (live_session_id, account_id) REFERENCES runtime_session_identities (live_session_id, account_id)
        ON UPDATE RESTRICT ON DELETE RESTRICT,
    CONSTRAINT chk_runtime_event_dedup_receipts_expiry CHECK (expires_at > created_at)
) ENGINE=InnoDB DEFAULT CHARACTER SET utf8mb4 COLLATE utf8mb4_0900_ai_ci;

INSERT INTO runtime_session_identities (live_session_id, account_id)
SELECT source.live_session_id, source.source_account_id
FROM (
    SELECT id AS live_session_id, account_id AS source_account_id
    FROM live_sessions
) AS source
ON DUPLICATE KEY UPDATE
    account_id = IF(runtime_session_identities.account_id = source.source_account_id, runtime_session_identities.account_id, NULL);

INSERT INTO runtime_active_session_guards (account_id, live_session_id)
SELECT source.source_account_id, source.live_session_id
FROM (
    SELECT account_id AS source_account_id, id AS live_session_id
    FROM live_sessions
    WHERE ended_at IS NULL
) AS source
ON DUPLICATE KEY UPDATE
    live_session_id = IF(runtime_active_session_guards.account_id = source.source_account_id AND runtime_active_session_guards.live_session_id = source.live_session_id, runtime_active_session_guards.live_session_id, NULL);
