CREATE TABLE IF NOT EXISTS admin_operation_authorizations (
    id BIGINT UNSIGNED NOT NULL AUTO_INCREMENT,
    token_hash BINARY(32) NOT NULL,
    session_id BIGINT UNSIGNED NOT NULL,
    credential_epoch BIGINT UNSIGNED NOT NULL,
    purpose VARCHAR(64) NOT NULL,
    target VARCHAR(256) NOT NULL,
    totp_step TIMESTAMP(6) NOT NULL,
    created_at TIMESTAMP(6) NOT NULL,
    expires_at TIMESTAMP(6) NOT NULL,
    consumed_at TIMESTAMP(6) NULL,
    PRIMARY KEY (id),
    UNIQUE KEY uq_admin_operation_authorizations_token_hash (token_hash),
    UNIQUE KEY uq_admin_operation_authorizations_totp_step (credential_epoch, totp_step),
    KEY idx_admin_operation_authorizations_expiry (expires_at),
    KEY idx_admin_operation_authorizations_session (session_id),
    CONSTRAINT fk_admin_operation_authorizations_session
        FOREIGN KEY (session_id) REFERENCES site_sessions (id)
        ON UPDATE RESTRICT ON DELETE RESTRICT,
    CONSTRAINT chk_admin_operation_authorizations_epoch CHECK (credential_epoch >= 1),
    CONSTRAINT chk_admin_operation_authorizations_expiry CHECK (expires_at > created_at),
    CONSTRAINT chk_admin_operation_authorizations_consumed CHECK (consumed_at IS NULL OR consumed_at >= created_at)
) ENGINE=InnoDB DEFAULT CHARACTER SET utf8mb4 COLLATE utf8mb4_0900_ai_ci;
