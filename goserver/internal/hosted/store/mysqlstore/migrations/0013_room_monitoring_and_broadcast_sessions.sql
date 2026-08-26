-- A broadcast is the room-level business session. live_sessions remains the
-- per-account execution-session table and links to this new boundary.

CREATE TABLE IF NOT EXISTS broadcast_sessions (
    id BIGINT UNSIGNED NOT NULL AUTO_INCREMENT,
    room_id VARCHAR(20) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
    started_at TIMESTAMP(6) NOT NULL,
    ended_at TIMESTAMP(6) NULL,
    open_room_id VARCHAR(20) CHARACTER SET ascii COLLATE ascii_bin GENERATED ALWAYS AS (CASE WHEN ended_at IS NULL THEN room_id ELSE NULL END) STORED,
    PRIMARY KEY (id),
    UNIQUE KEY uq_broadcast_sessions_open_room (open_room_id),
    KEY idx_broadcast_sessions_room_started (room_id, started_at),
    CONSTRAINT chk_broadcast_sessions_room_id CHECK (room_id REGEXP '^[1-9][0-9]{0,19}$'),
    CONSTRAINT chk_broadcast_sessions_end_after_start CHECK (ended_at IS NULL OR ended_at >= started_at)
) ENGINE=InnoDB DEFAULT CHARACTER SET utf8mb4 COLLATE utf8mb4_0900_ai_ci;

CREATE TABLE IF NOT EXISTS room_monitor_states (
    room_id VARCHAR(20) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
    state VARCHAR(16) CHARACTER SET ascii COLLATE ascii_bin NOT NULL DEFAULT 'offline',
    grace_until TIMESTAMP(6) NULL,
    broadcast_session_id BIGINT UNSIGNED NULL,
    lease_epoch BIGINT UNSIGNED NOT NULL DEFAULT 1,
    updated_at TIMESTAMP(6) NOT NULL DEFAULT CURRENT_TIMESTAMP(6),
    PRIMARY KEY (room_id),
    KEY idx_room_monitor_states_recovery (state, grace_until, room_id),
    KEY idx_room_monitor_states_broadcast (broadcast_session_id),
    CONSTRAINT fk_room_monitor_states_broadcast_session FOREIGN KEY (broadcast_session_id) REFERENCES broadcast_sessions (id)
        ON UPDATE RESTRICT ON DELETE RESTRICT,
    CONSTRAINT chk_room_monitor_states_room_id CHECK (room_id REGEXP '^[1-9][0-9]{0,19}$'),
    CONSTRAINT chk_room_monitor_states_state CHECK (state IN ('offline', 'live', 'grace')),
    CONSTRAINT chk_room_monitor_states_epoch CHECK (lease_epoch >= 1),
    CONSTRAINT chk_room_monitor_states_shape CHECK (
        (state = 'offline' AND broadcast_session_id IS NULL AND grace_until IS NULL)
        OR (state = 'live' AND broadcast_session_id IS NOT NULL AND grace_until IS NULL)
        OR (state = 'grace' AND broadcast_session_id IS NOT NULL AND grace_until IS NOT NULL)
    )
) ENGINE=InnoDB DEFAULT CHARACTER SET utf8mb4 COLLATE utf8mb4_0900_ai_ci;

CREATE TABLE IF NOT EXISTS room_monitor_references (
    account_id BIGINT UNSIGNED NOT NULL,
    room_id VARCHAR(20) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
    created_at TIMESTAMP(6) NOT NULL DEFAULT CURRENT_TIMESTAMP(6),
    PRIMARY KEY (account_id, room_id),
    KEY idx_room_monitor_references_room_account (room_id, account_id),
    CONSTRAINT fk_room_monitor_references_account FOREIGN KEY (account_id) REFERENCES streamer_accounts (id)
        ON UPDATE RESTRICT ON DELETE RESTRICT,
    CONSTRAINT fk_room_monitor_references_state FOREIGN KEY (room_id) REFERENCES room_monitor_states (room_id)
        ON UPDATE RESTRICT ON DELETE RESTRICT
) ENGINE=InnoDB DEFAULT CHARACTER SET utf8mb4 COLLATE utf8mb4_0900_ai_ci;

CREATE TABLE IF NOT EXISTS room_monitor_transitions (
    sequence BIGINT UNSIGNED NOT NULL,
    event_kind VARCHAR(32) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
    room_id VARCHAR(20) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
    lease_epoch BIGINT UNSIGNED NULL,
    from_state VARCHAR(16) CHARACTER SET ascii COLLATE ascii_bin NULL,
    to_state VARCHAR(16) CHARACTER SET ascii COLLATE ascii_bin NULL,
    confirmed_at TIMESTAMP(6) NULL,
    grace_until TIMESTAMP(6) NULL,
    new_broadcast TINYINT NULL,
    account_ids_json JSON NULL,
    PRIMARY KEY (sequence),
    KEY idx_room_monitor_transitions_room_sequence (room_id, sequence),
    CONSTRAINT fk_room_monitor_transitions_state FOREIGN KEY (room_id) REFERENCES room_monitor_states (room_id)
        ON UPDATE RESTRICT ON DELETE RESTRICT,
    CONSTRAINT chk_room_monitor_transitions_kind CHECK (event_kind IN ('room_state_changed', 'room_references_changed')),
    CONSTRAINT chk_room_monitor_transitions_epoch CHECK (lease_epoch IS NULL OR lease_epoch >= 1),
    CONSTRAINT chk_room_monitor_transitions_from CHECK (from_state IS NULL OR from_state IN ('offline', 'live', 'grace')),
    CONSTRAINT chk_room_monitor_transitions_to CHECK (to_state IS NULL OR to_state IN ('offline', 'live', 'grace')),
    CONSTRAINT chk_room_monitor_transitions_change CHECK (from_state IS NULL OR to_state IS NULL OR from_state <> to_state),
    CONSTRAINT chk_room_monitor_transitions_broadcast CHECK (new_broadcast IS NULL OR new_broadcast IN (0, 1)),
    CONSTRAINT chk_room_monitor_transitions_payload CHECK (
        (event_kind = 'room_state_changed'
            AND lease_epoch IS NOT NULL AND from_state IS NOT NULL AND to_state IS NOT NULL
            AND confirmed_at IS NOT NULL AND new_broadcast IS NOT NULL AND account_ids_json IS NULL)
        OR (event_kind = 'room_references_changed'
            AND lease_epoch IS NULL AND from_state IS NULL AND to_state IS NULL
            AND confirmed_at IS NULL AND grace_until IS NULL AND new_broadcast IS NULL
            AND account_ids_json IS NOT NULL AND JSON_TYPE(account_ids_json) = 'ARRAY')
    )
) ENGINE=InnoDB DEFAULT CHARACTER SET utf8mb4 COLLATE utf8mb4_0900_ai_ci;

-- Every room event takes this row first, then its room/reference rows. The
-- reference snapshot and state payloads therefore share one commit order.
-- allocator ensures a committed cursor never observes a later sequence while
-- an earlier one remains uncommitted in another transaction.
CREATE TABLE IF NOT EXISTS room_monitor_outbox_tail (
    id TINYINT UNSIGNED NOT NULL,
    next_sequence BIGINT UNSIGNED NOT NULL,
    PRIMARY KEY (id),
    CONSTRAINT chk_room_monitor_outbox_tail_id CHECK (id = 1),
    CONSTRAINT chk_room_monitor_outbox_tail_sequence CHECK (next_sequence >= 1)
) ENGINE=InnoDB DEFAULT CHARACTER SET utf8mb4 COLLATE utf8mb4_0900_ai_ci;

INSERT IGNORE INTO room_monitor_outbox_tail (id, next_sequence) VALUES (1, 1);

SET @room_monitor_live_broadcast_column_state := (
    SELECT CASE
        WHEN COUNT(*) = 0 THEN 'absent'
        WHEN COUNT(*) = 1
             AND MAX(column_type = 'bigint unsigned') = 1
             AND MAX(is_nullable = 'YES') = 1 THEN 'matching'
        ELSE 'mismatch'
    END
    FROM information_schema.columns
    WHERE table_schema = DATABASE() AND table_name = 'live_sessions' AND column_name = 'broadcast_session_id'
);

SET @room_monitor_live_broadcast_column_sql := CASE @room_monitor_live_broadcast_column_state
    WHEN 'absent' THEN 'ALTER TABLE live_sessions ADD COLUMN broadcast_session_id BIGINT UNSIGNED NULL AFTER id'
    WHEN 'matching' THEN 'SELECT 1'
    ELSE 'SELECT * FROM information_schema.room_monitor_live_broadcast_column_definition_mismatch'
END;

PREPARE room_monitor_live_broadcast_column_stmt FROM @room_monitor_live_broadcast_column_sql;
EXECUTE room_monitor_live_broadcast_column_stmt;
DEALLOCATE PREPARE room_monitor_live_broadcast_column_stmt;

SET @room_monitor_live_broadcast_index_sql := IF(
    EXISTS (
        SELECT 1 FROM information_schema.statistics
        WHERE table_schema = DATABASE() AND table_name = 'live_sessions' AND index_name = 'idx_live_sessions_broadcast_session'
        GROUP BY index_name HAVING COUNT(*) = 1 AND MAX(seq_in_index = 1 AND column_name = 'broadcast_session_id') = 1
    ),
    'SELECT 1',
    'ALTER TABLE live_sessions ADD KEY idx_live_sessions_broadcast_session (broadcast_session_id)'
);

PREPARE room_monitor_live_broadcast_index_stmt FROM @room_monitor_live_broadcast_index_sql;
EXECUTE room_monitor_live_broadcast_index_stmt;
DEALLOCATE PREPARE room_monitor_live_broadcast_index_stmt;

SET @room_monitor_live_broadcast_fk_sql := IF(
    EXISTS (
        SELECT 1 FROM information_schema.referential_constraints
        WHERE constraint_schema = DATABASE() AND table_name = 'live_sessions' AND constraint_name = 'fk_live_sessions_broadcast_session'
    ),
    'SELECT 1',
    'ALTER TABLE live_sessions ADD CONSTRAINT fk_live_sessions_broadcast_session FOREIGN KEY (broadcast_session_id) REFERENCES broadcast_sessions (id) ON UPDATE RESTRICT ON DELETE SET NULL'
);

PREPARE room_monitor_live_broadcast_fk_stmt FROM @room_monitor_live_broadcast_fk_sql;
EXECUTE room_monitor_live_broadcast_fk_stmt;
DEALLOCATE PREPARE room_monitor_live_broadcast_fk_stmt;
