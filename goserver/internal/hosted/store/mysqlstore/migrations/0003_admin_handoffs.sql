CREATE TABLE IF NOT EXISTS admin_credential_handoffs (
    id BIGINT UNSIGNED NOT NULL AUTO_INCREMENT,
    handoff_kind VARCHAR(16) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
    handoff_state VARCHAR(16) CHARACTER SET ascii COLLATE ascii_bin NOT NULL DEFAULT 'pending',
    request_hash BINARY(32) NOT NULL,
    token_hash BINARY(32) NOT NULL,
    token_ciphertext VARBINARY(512) NULL,
    admin_identity_id TINYINT UNSIGNED NULL,
    reserved_recovery_code_id BIGINT UNSIGNED NULL,
    uid_ciphertext VARBINARY(512) NULL,
    uid_lookup BINARY(32) NULL,
    email_ciphertext VARBINARY(1024) NOT NULL,
    totp_secret_ciphertext VARBINARY(512) NULL,
    totp_uri_ciphertext VARBINARY(2048) NULL,
    archive_password_ciphertext VARBINARY(512) NULL,
    recovery_archive LONGBLOB NULL,
    created_at TIMESTAMP(6) NOT NULL,
    expires_at TIMESTAMP(6) NOT NULL,
    confirmed_at TIMESTAMP(6) NULL,
    mail_attempt_count INT UNSIGNED NOT NULL DEFAULT 0,
    last_mail_attempt_at TIMESTAMP(6) NULL,
    mail_delivered_at TIMESTAMP(6) NULL,
    pending_initialization_guard TINYINT UNSIGNED GENERATED ALWAYS AS (
        CASE WHEN handoff_kind = 'initialization' AND handoff_state = 'pending' THEN 1 ELSE NULL END
    ) STORED,
    pending_request_hash BINARY(32) GENERATED ALWAYS AS (
        CASE WHEN handoff_state = 'pending' THEN request_hash ELSE NULL END
    ) STORED,
    pending_reserved_recovery_code_id BIGINT UNSIGNED GENERATED ALWAYS AS (
        CASE WHEN handoff_kind = 'recovery' AND handoff_state = 'pending' THEN reserved_recovery_code_id ELSE NULL END
    ) STORED,
    pending_recovery_admin_guard TINYINT UNSIGNED GENERATED ALWAYS AS (
        CASE WHEN handoff_kind = 'recovery' AND handoff_state = 'pending' THEN admin_identity_id ELSE NULL END
    ) STORED,
    PRIMARY KEY (id),
    UNIQUE KEY uq_admin_handoffs_token_hash (token_hash),
    UNIQUE KEY uq_admin_handoffs_pending_initialization (pending_initialization_guard),
    UNIQUE KEY uq_admin_handoffs_pending_request (pending_request_hash),
    UNIQUE KEY uq_admin_handoffs_pending_recovery_code (pending_reserved_recovery_code_id),
    UNIQUE KEY uq_admin_handoffs_pending_recovery_admin (pending_recovery_admin_guard),
    KEY idx_admin_handoffs_expiry (handoff_state, expires_at, id),
    KEY idx_admin_handoffs_admin (admin_identity_id),
    CONSTRAINT fk_admin_handoffs_identity
        FOREIGN KEY (admin_identity_id) REFERENCES admin_identity (id)
        ON UPDATE RESTRICT ON DELETE RESTRICT,
    CONSTRAINT fk_admin_handoffs_reserved_code
        FOREIGN KEY (reserved_recovery_code_id) REFERENCES admin_recovery_codes (id)
        ON UPDATE RESTRICT ON DELETE RESTRICT,
    CONSTRAINT chk_admin_handoffs_kind CHECK (handoff_kind IN ('initialization', 'recovery')),
    CONSTRAINT chk_admin_handoffs_state CHECK (handoff_state IN ('pending', 'confirmed', 'expired')),
    CONSTRAINT chk_admin_handoffs_expiry CHECK (expires_at > created_at),
    CONSTRAINT chk_admin_handoffs_recovery_owner CHECK (
        (handoff_kind = 'initialization' AND admin_identity_id IS NULL AND reserved_recovery_code_id IS NULL)
        OR (handoff_kind = 'recovery' AND admin_identity_id = 1 AND reserved_recovery_code_id IS NOT NULL)
    )
) ENGINE=InnoDB DEFAULT CHARACTER SET utf8mb4 COLLATE utf8mb4_0900_ai_ci;

CREATE TABLE IF NOT EXISTS admin_handoff_recovery_codes (
    handoff_id BIGINT UNSIGNED NOT NULL,
    code_ordinal TINYINT UNSIGNED NOT NULL,
    code_hash BINARY(32) NOT NULL,
    PRIMARY KEY (handoff_id, code_ordinal),
    UNIQUE KEY uq_admin_handoff_recovery_codes_hash (code_hash),
    CONSTRAINT fk_admin_handoff_recovery_codes_handoff
        FOREIGN KEY (handoff_id) REFERENCES admin_credential_handoffs (id)
        ON UPDATE RESTRICT ON DELETE CASCADE,
    CONSTRAINT chk_admin_handoff_recovery_codes_ordinal CHECK (code_ordinal < 10)
) ENGINE=InnoDB DEFAULT CHARACTER SET utf8mb4 COLLATE utf8mb4_0900_ai_ci;
