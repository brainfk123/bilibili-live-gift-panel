SET @invitation_ciphertext_column_state := (
    SELECT CASE
        WHEN COUNT(*) = 0 THEN 'absent'
        WHEN COUNT(*) = 1
            AND MAX(DATA_TYPE) = 'varbinary'
            AND MAX(CHARACTER_MAXIMUM_LENGTH) = 512
            AND MAX(IS_NULLABLE) = 'YES'
        THEN 'exact'
        ELSE 'mismatch'
    END
    FROM information_schema.columns
    WHERE table_schema = DATABASE()
      AND table_name = 'invitations'
      AND column_name = 'code_ciphertext'
);

SET @invitation_ciphertext_column_sql := CASE @invitation_ciphertext_column_state
    WHEN 'absent' THEN 'ALTER TABLE invitations ADD COLUMN code_ciphertext VARBINARY(512) NULL AFTER code_hash'
    WHEN 'exact' THEN 'DO 0'
    ELSE 'SELECT * FROM information_schema.invitation_ciphertext_column_definition_mismatch'
END;

PREPARE invitation_ciphertext_column_stmt FROM @invitation_ciphertext_column_sql;
EXECUTE invitation_ciphertext_column_stmt;
DEALLOCATE PREPARE invitation_ciphertext_column_stmt;

ALTER TABLE invitations MODIFY COLUMN expires_at TIMESTAMP(6) NULL;
