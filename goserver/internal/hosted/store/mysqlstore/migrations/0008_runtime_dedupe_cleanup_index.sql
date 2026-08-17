SET @runtime_dedupe_cleanup_index_state := (
    SELECT CASE
        WHEN COUNT(*) = 0 THEN 'absent'
        WHEN COUNT(*) = 3
            AND SUM(CASE WHEN SEQ_IN_INDEX = 1 AND COLUMN_NAME = 'account_id' THEN 1 ELSE 0 END) = 1
            AND SUM(CASE WHEN SEQ_IN_INDEX = 2 AND COLUMN_NAME = 'expires_at' THEN 1 ELSE 0 END) = 1
            AND SUM(CASE WHEN SEQ_IN_INDEX = 3 AND COLUMN_NAME = 'event_hash' THEN 1 ELSE 0 END) = 1
            AND SUM(CASE WHEN NON_UNIQUE = 1 THEN 1 ELSE 0 END) = 3
            AND SUM(CASE WHEN SUB_PART IS NULL THEN 1 ELSE 0 END) = 3
            AND SUM(CASE WHEN INDEX_TYPE = 'BTREE' THEN 1 ELSE 0 END) = 3
            AND SUM(CASE WHEN IS_VISIBLE = 'YES' THEN 1 ELSE 0 END) = 3
            AND SUM(CASE WHEN COLLATION = 'A' THEN 1 ELSE 0 END) = 3
        THEN 'exact'
        ELSE 'mismatch'
    END
    FROM information_schema.statistics
    WHERE table_schema = DATABASE()
      AND table_name = 'runtime_event_dedup_receipts'
      AND index_name = 'idx_runtime_event_dedup_receipts_account_expiry'
);

SET @runtime_dedupe_cleanup_index_sql := CASE @runtime_dedupe_cleanup_index_state
    WHEN 'absent' THEN 'CREATE INDEX idx_runtime_event_dedup_receipts_account_expiry ON runtime_event_dedup_receipts (account_id, expires_at, event_hash)'
    WHEN 'exact' THEN 'DO 0'
    ELSE 'SELECT * FROM information_schema.runtime_dedupe_cleanup_index_definition_mismatch'
END;

PREPARE runtime_dedupe_cleanup_index_stmt FROM @runtime_dedupe_cleanup_index_sql;
EXECUTE runtime_dedupe_cleanup_index_stmt;
DEALLOCATE PREPARE runtime_dedupe_cleanup_index_stmt;
