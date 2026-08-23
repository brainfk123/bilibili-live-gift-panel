SET @admin_session_public_id_column_state := (
    SELECT CASE
        WHEN COUNT(*) = 0 THEN 'absent'
        WHEN COUNT(*) = 1
            AND MAX(DATA_TYPE) = 'binary'
            AND MAX(CHARACTER_MAXIMUM_LENGTH) = 16
        THEN 'exact'
        ELSE 'mismatch'
    END
    FROM information_schema.columns
    WHERE table_schema = DATABASE()
      AND table_name = 'site_sessions'
      AND column_name = 'public_id'
);

SET @admin_session_public_id_column_sql := CASE @admin_session_public_id_column_state
    WHEN 'absent' THEN 'ALTER TABLE site_sessions ADD COLUMN public_id BINARY(16) NULL AFTER id'
    WHEN 'exact' THEN 'DO 0'
    ELSE 'SELECT * FROM information_schema.admin_session_public_id_column_definition_mismatch'
END;

PREPARE admin_session_public_id_column_stmt FROM @admin_session_public_id_column_sql;
EXECUTE admin_session_public_id_column_stmt;
DEALLOCATE PREPARE admin_session_public_id_column_stmt;

SET @admin_session_device_label_column_state := (
    SELECT CASE
        WHEN COUNT(*) = 0 THEN 'absent'
        WHEN COUNT(*) = 1
            AND MAX(DATA_TYPE) = 'varchar'
            AND MAX(CHARACTER_MAXIMUM_LENGTH) = 80
        THEN 'exact'
        ELSE 'mismatch'
    END
    FROM information_schema.columns
    WHERE table_schema = DATABASE()
      AND table_name = 'site_sessions'
      AND column_name = 'device_label'
);

SET @admin_session_device_label_column_sql := CASE @admin_session_device_label_column_state
    WHEN 'absent' THEN 'ALTER TABLE site_sessions ADD COLUMN device_label VARCHAR(80) NULL AFTER credential_epoch'
    WHEN 'exact' THEN 'DO 0'
    ELSE 'SELECT * FROM information_schema.admin_session_device_label_column_definition_mismatch'
END;

PREPARE admin_session_device_label_column_stmt FROM @admin_session_device_label_column_sql;
EXECUTE admin_session_device_label_column_stmt;
DEALLOCATE PREPARE admin_session_device_label_column_stmt;

SET @admin_session_client_network_column_state := (
    SELECT CASE
        WHEN COUNT(*) = 0 THEN 'absent'
        WHEN COUNT(*) = 1
            AND MAX(DATA_TYPE) = 'varchar'
            AND MAX(CHARACTER_MAXIMUM_LENGTH) = 64
        THEN 'exact'
        ELSE 'mismatch'
    END
    FROM information_schema.columns
    WHERE table_schema = DATABASE()
      AND table_name = 'site_sessions'
      AND column_name = 'client_network'
);

SET @admin_session_client_network_column_sql := CASE @admin_session_client_network_column_state
    WHEN 'absent' THEN 'ALTER TABLE site_sessions ADD COLUMN client_network VARCHAR(64) NULL AFTER device_label'
    WHEN 'exact' THEN 'DO 0'
    ELSE 'SELECT * FROM information_schema.admin_session_client_network_column_definition_mismatch'
END;

PREPARE admin_session_client_network_column_stmt FROM @admin_session_client_network_column_sql;
EXECUTE admin_session_client_network_column_stmt;
DEALLOCATE PREPARE admin_session_client_network_column_stmt;

SET @admin_session_last_seen_column_state := (
    SELECT CASE
        WHEN COUNT(*) = 0 THEN 'absent'
        WHEN COUNT(*) = 1
            AND MAX(DATA_TYPE) = 'datetime'
            AND MAX(DATETIME_PRECISION) = 6
        THEN 'exact'
        ELSE 'mismatch'
    END
    FROM information_schema.columns
    WHERE table_schema = DATABASE()
      AND table_name = 'site_sessions'
      AND column_name = 'last_seen_at'
);

SET @admin_session_last_seen_column_sql := CASE @admin_session_last_seen_column_state
    WHEN 'absent' THEN 'ALTER TABLE site_sessions ADD COLUMN last_seen_at DATETIME(6) NULL AFTER created_at'
    WHEN 'exact' THEN 'DO 0'
    ELSE 'SELECT * FROM information_schema.admin_session_last_seen_column_definition_mismatch'
END;

PREPARE admin_session_last_seen_column_stmt FROM @admin_session_last_seen_column_sql;
EXECUTE admin_session_last_seen_column_stmt;
DEALLOCATE PREPARE admin_session_last_seen_column_stmt;

UPDATE site_sessions
SET public_id = COALESCE(public_id, UUID_TO_BIN(UUID())),
    device_label = COALESCE(device_label, '其他设备 · 其他浏览器'),
    client_network = COALESCE(client_network, '—'),
    last_seen_at = COALESCE(last_seen_at, created_at)
WHERE public_id IS NULL
   OR device_label IS NULL
   OR client_network IS NULL
   OR last_seen_at IS NULL;

ALTER TABLE site_sessions
    MODIFY COLUMN public_id BINARY(16) NOT NULL DEFAULT (UUID_TO_BIN(UUID())),
    MODIFY COLUMN device_label VARCHAR(80) NOT NULL DEFAULT '其他设备 · 其他浏览器',
    MODIFY COLUMN client_network VARCHAR(64) NOT NULL DEFAULT '—',
    MODIFY COLUMN last_seen_at DATETIME(6) NOT NULL DEFAULT CURRENT_TIMESTAMP(6);

SET @admin_session_public_id_index_sql := IF(
    EXISTS (
        SELECT 1
        FROM information_schema.statistics
        WHERE table_schema = DATABASE()
          AND table_name = 'site_sessions'
          AND index_name = 'uq_site_sessions_public_id'
    ),
    'DO 0',
    'ALTER TABLE site_sessions ADD UNIQUE KEY uq_site_sessions_public_id (public_id)'
);

PREPARE admin_session_public_id_index_stmt FROM @admin_session_public_id_index_sql;
EXECUTE admin_session_public_id_index_stmt;
DEALLOCATE PREPARE admin_session_public_id_index_stmt;

SET @admin_session_activity_index_sql := IF(
    EXISTS (
        SELECT 1
        FROM information_schema.statistics
        WHERE table_schema = DATABASE()
          AND table_name = 'site_sessions'
          AND index_name = 'idx_site_sessions_admin_activity'
    ),
    'DO 0',
    'ALTER TABLE site_sessions ADD KEY idx_site_sessions_admin_activity (admin_identity_id, revoked_at, last_seen_at DESC)'
);

PREPARE admin_session_activity_index_stmt FROM @admin_session_activity_index_sql;
EXECUTE admin_session_activity_index_stmt;
DEALLOCATE PREPARE admin_session_activity_index_stmt;

CREATE TABLE IF NOT EXISTS admin_login_events (
    id BIGINT UNSIGNED NOT NULL AUTO_INCREMENT,
    result VARCHAR(16) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
    device_label VARCHAR(80) NOT NULL,
    client_network VARCHAR(64) NOT NULL,
    occurred_at DATETIME(6) NOT NULL,
    PRIMARY KEY (id),
    KEY idx_admin_login_events_occurred (occurred_at DESC, id DESC),
    CONSTRAINT chk_admin_login_events_result CHECK (result IN ('success', 'failure'))
) ENGINE=InnoDB DEFAULT CHARACTER SET utf8mb4 COLLATE utf8mb4_0900_ai_ci;
