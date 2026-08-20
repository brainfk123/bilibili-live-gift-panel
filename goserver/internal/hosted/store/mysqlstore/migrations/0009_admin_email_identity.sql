ALTER TABLE admin_identity
    MODIFY COLUMN uid_ciphertext VARBINARY(512) NULL,
    MODIFY COLUMN uid_lookup BINARY(32) NULL;
