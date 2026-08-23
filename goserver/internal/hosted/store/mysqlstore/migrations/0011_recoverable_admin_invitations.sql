ALTER TABLE invitations
    ADD COLUMN IF NOT EXISTS code_ciphertext VARBINARY(512) NULL AFTER code_hash,
    MODIFY COLUMN expires_at TIMESTAMP(6) NULL;
