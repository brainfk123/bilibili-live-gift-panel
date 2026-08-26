-- Forward-only durable administrator room-mutation receipts. Published room
-- monitoring migrations 0013 and 0014 remain byte-for-byte unchanged.

CREATE TABLE IF NOT EXISTS room_mutation_receipts (
    id BIGINT UNSIGNED NOT NULL AUTO_INCREMENT,
    mutation_id BINARY(16) NOT NULL,
    account_id BIGINT UNSIGNED NOT NULL,
    desired_room_id VARCHAR(20) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
    old_room_id VARCHAR(20) CHARACTER SET ascii COLLATE ascii_bin NULL,
    new_room_id VARCHAR(20) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
    phase VARCHAR(32) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
    audit_event_id BIGINT UNSIGNED NULL,
    created_at TIMESTAMP(6) NOT NULL,
    updated_at TIMESTAMP(6) NOT NULL,
    active_account_id BIGINT UNSIGNED GENERATED ALWAYS AS (
        CASE WHEN phase <> 'audited' THEN account_id ELSE NULL END
    ) STORED,
    PRIMARY KEY (id),
    UNIQUE KEY uq_room_mutation_receipts_mutation (mutation_id),
    UNIQUE KEY uq_room_mutation_receipts_active_account (active_account_id),
    UNIQUE KEY uq_room_mutation_receipts_audit (audit_event_id),
    KEY idx_room_mutation_receipts_account_created (account_id, created_at, id),
    CONSTRAINT fk_room_mutation_receipts_account
        FOREIGN KEY (account_id) REFERENCES streamer_accounts (id)
        ON UPDATE RESTRICT ON DELETE RESTRICT,
    CONSTRAINT fk_room_mutation_receipts_audit
        FOREIGN KEY (audit_event_id) REFERENCES audit_events (id)
        ON UPDATE RESTRICT ON DELETE RESTRICT,
    CONSTRAINT chk_room_mutation_receipts_desired CHECK (desired_room_id REGEXP '^[1-9][0-9]{0,19}$'),
    CONSTRAINT chk_room_mutation_receipts_old CHECK (old_room_id IS NULL OR old_room_id REGEXP '^[1-9][0-9]{0,19}$'),
    CONSTRAINT chk_room_mutation_receipts_new CHECK (new_room_id REGEXP '^[1-9][0-9]{0,19}$'),
    CONSTRAINT chk_room_mutation_receipts_phase CHECK (phase IN ('target_persisted', 'references_synced', 'audited')),
    CONSTRAINT chk_room_mutation_receipts_audit_phase CHECK ((phase = 'audited') = (audit_event_id IS NOT NULL))
) ENGINE=InnoDB DEFAULT CHARACTER SET utf8mb4 COLLATE utf8mb4_0900_ai_ci;
