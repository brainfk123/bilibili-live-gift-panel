-- Forward-only expansion of the published 0013 transition outbox. Existing
-- rows are state events; reference snapshots become the second payload kind.

SET @room_event_kind_column_state := (
    SELECT CASE
        WHEN COUNT(*) = 0 THEN 'absent'
        WHEN COUNT(*) = 1
             AND MAX(DATA_TYPE) = 'varchar'
             AND MAX(CHARACTER_MAXIMUM_LENGTH) = 32
             AND MAX(CHARACTER_SET_NAME) = 'ascii'
             AND MAX(COLLATION_NAME) = 'ascii_bin' THEN 'compatible'
        ELSE 'mismatch'
    END
    FROM information_schema.columns
    WHERE table_schema = DATABASE()
      AND table_name = 'room_monitor_transitions'
      AND column_name = 'event_kind'
);

SET @room_event_kind_column_sql := CASE @room_event_kind_column_state
    WHEN 'absent' THEN 'ALTER TABLE room_monitor_transitions ADD COLUMN event_kind VARCHAR(32) CHARACTER SET ascii COLLATE ascii_bin NULL AFTER sequence'
    WHEN 'compatible' THEN 'DO 0'
    ELSE 'SELECT * FROM information_schema.room_event_kind_column_definition_mismatch'
END;

PREPARE room_event_kind_column_stmt FROM @room_event_kind_column_sql;
EXECUTE room_event_kind_column_stmt;
DEALLOCATE PREPARE room_event_kind_column_stmt;

SET @room_event_accounts_column_state := (
    SELECT CASE
        WHEN COUNT(*) = 0 THEN 'absent'
        WHEN COUNT(*) = 1 AND MAX(DATA_TYPE) = 'json' AND MAX(IS_NULLABLE) = 'YES' THEN 'exact'
        ELSE 'mismatch'
    END
    FROM information_schema.columns
    WHERE table_schema = DATABASE()
      AND table_name = 'room_monitor_transitions'
      AND column_name = 'account_ids_json'
);

SET @room_event_accounts_column_sql := CASE @room_event_accounts_column_state
    WHEN 'absent' THEN 'ALTER TABLE room_monitor_transitions ADD COLUMN account_ids_json JSON NULL AFTER new_broadcast'
    WHEN 'exact' THEN 'DO 0'
    ELSE 'SELECT * FROM information_schema.room_event_accounts_column_definition_mismatch'
END;

PREPARE room_event_accounts_column_stmt FROM @room_event_accounts_column_sql;
EXECUTE room_event_accounts_column_stmt;
DEALLOCATE PREPARE room_event_accounts_column_stmt;

UPDATE room_monitor_transitions
SET event_kind = 'room_state_changed'
WHERE event_kind IS NULL;

ALTER TABLE room_monitor_transitions
    MODIFY COLUMN event_kind VARCHAR(32) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
    MODIFY COLUMN lease_epoch BIGINT UNSIGNED NULL,
    MODIFY COLUMN from_state VARCHAR(16) CHARACTER SET ascii COLLATE ascii_bin NULL,
    MODIFY COLUMN to_state VARCHAR(16) CHARACTER SET ascii COLLATE ascii_bin NULL,
    MODIFY COLUMN confirmed_at TIMESTAMP(6) NULL,
    MODIFY COLUMN new_broadcast TINYINT NULL;

SET @room_event_kind_check_sql := IF(
    EXISTS (
        SELECT 1 FROM information_schema.table_constraints
        WHERE constraint_schema = DATABASE()
          AND table_name = 'room_monitor_transitions'
          AND constraint_name = 'chk_room_monitor_transitions_kind'
          AND constraint_type = 'CHECK'
    ),
    'DO 0',
    'ALTER TABLE room_monitor_transitions ADD CONSTRAINT chk_room_monitor_transitions_kind CHECK (event_kind IN (''room_state_changed'', ''room_references_changed''))'
);

PREPARE room_event_kind_check_stmt FROM @room_event_kind_check_sql;
EXECUTE room_event_kind_check_stmt;
DEALLOCATE PREPARE room_event_kind_check_stmt;

SET @room_event_payload_check_sql := IF(
    EXISTS (
        SELECT 1 FROM information_schema.table_constraints
        WHERE constraint_schema = DATABASE()
          AND table_name = 'room_monitor_transitions'
          AND constraint_name = 'chk_room_monitor_transitions_payload'
          AND constraint_type = 'CHECK'
    ),
    'DO 0',
    'ALTER TABLE room_monitor_transitions ADD CONSTRAINT chk_room_monitor_transitions_payload CHECK ((event_kind = ''room_state_changed'' AND lease_epoch IS NOT NULL AND from_state IS NOT NULL AND to_state IS NOT NULL AND confirmed_at IS NOT NULL AND new_broadcast IS NOT NULL AND account_ids_json IS NULL) OR (event_kind = ''room_references_changed'' AND lease_epoch IS NULL AND from_state IS NULL AND to_state IS NULL AND confirmed_at IS NULL AND grace_until IS NULL AND new_broadcast IS NULL AND account_ids_json IS NOT NULL AND JSON_TYPE(account_ids_json) = ''ARRAY''))'
);

PREPARE room_event_payload_check_stmt FROM @room_event_payload_check_sql;
EXECUTE room_event_payload_check_stmt;
DEALLOCATE PREPARE room_event_payload_check_stmt;
