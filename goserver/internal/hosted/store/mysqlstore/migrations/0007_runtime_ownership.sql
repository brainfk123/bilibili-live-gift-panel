-- Cross-process runtime ownership. The row is retained after release so every
-- later claim advances the fencing epoch instead of resetting it.
CREATE TABLE IF NOT EXISTS runtime_account_owners (
    account_id BIGINT UNSIGNED NOT NULL,
    owner_token BINARY(32) NOT NULL,
    fencing_epoch BIGINT UNSIGNED NOT NULL,
    expires_at TIMESTAMP(6) NOT NULL,
    updated_at TIMESTAMP(6) NOT NULL DEFAULT CURRENT_TIMESTAMP(6) ON UPDATE CURRENT_TIMESTAMP(6),
    PRIMARY KEY (account_id),
    KEY idx_runtime_account_owners_expiry (expires_at, account_id),
    CONSTRAINT fk_runtime_account_owners_account
        FOREIGN KEY (account_id) REFERENCES streamer_accounts (id)
        ON UPDATE RESTRICT ON DELETE RESTRICT,
    CONSTRAINT chk_runtime_account_owners_epoch CHECK (fencing_epoch > 0)
) ENGINE=InnoDB DEFAULT CHARACTER SET utf8mb4 COLLATE utf8mb4_0900_ai_ci;

-- Existing open sessions predate ownership fencing. Give only those accounts
-- an already-expired bootstrap row so their first claimant performs a real
-- epoch takeover before reconciling the orphan lifecycle.
INSERT IGNORE INTO runtime_account_owners (account_id, owner_token, fencing_epoch, expires_at)
SELECT DISTINCT account_id,
       UNHEX(SHA2(CONCAT('runtime-owner-bootstrap:', account_id), 256)),
       1,
       UTC_TIMESTAMP(6)
FROM live_sessions
WHERE ended_at IS NULL;
