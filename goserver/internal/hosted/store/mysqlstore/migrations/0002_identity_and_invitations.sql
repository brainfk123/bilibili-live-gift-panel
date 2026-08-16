CREATE TABLE IF NOT EXISTS streamer_accounts (
    id BIGINT UNSIGNED NOT NULL AUTO_INCREMENT,
    credential_epoch BIGINT UNSIGNED NOT NULL DEFAULT 1,
    disabled_at TIMESTAMP(6) NULL,
    created_at TIMESTAMP(6) NOT NULL DEFAULT CURRENT_TIMESTAMP(6),
    updated_at TIMESTAMP(6) NOT NULL DEFAULT CURRENT_TIMESTAMP(6) ON UPDATE CURRENT_TIMESTAMP(6),
    PRIMARY KEY (id),
    CONSTRAINT chk_streamer_accounts_credential_epoch CHECK (credential_epoch >= 1)
) ENGINE=InnoDB DEFAULT CHARACTER SET utf8mb4 COLLATE utf8mb4_0900_ai_ci;

CREATE TABLE IF NOT EXISTS bili_uid_bindings (
    id BIGINT UNSIGNED NOT NULL AUTO_INCREMENT,
    account_id BIGINT UNSIGNED NOT NULL,
    uid_ciphertext VARBINARY(512) NOT NULL,
    uid_lookup BINARY(32) NOT NULL,
    bound_at TIMESTAMP(6) NOT NULL DEFAULT CURRENT_TIMESTAMP(6),
    unbound_at TIMESTAMP(6) NULL,
    active_account_id BIGINT UNSIGNED GENERATED ALWAYS AS (
        CASE WHEN unbound_at IS NULL THEN account_id ELSE NULL END
    ) STORED,
    PRIMARY KEY (id),
    UNIQUE KEY uq_bili_uid_bindings_uid_lookup (uid_lookup),
    UNIQUE KEY uq_bili_uid_bindings_active_account (active_account_id),
    KEY idx_bili_uid_bindings_account (account_id),
    CONSTRAINT fk_bili_uid_bindings_account
        FOREIGN KEY (account_id) REFERENCES streamer_accounts (id)
        ON UPDATE RESTRICT ON DELETE RESTRICT
) ENGINE=InnoDB DEFAULT CHARACTER SET utf8mb4 COLLATE utf8mb4_0900_ai_ci;

CREATE TABLE IF NOT EXISTS admin_identity (
    id TINYINT UNSIGNED NOT NULL DEFAULT 1,
    credential_epoch BIGINT UNSIGNED NOT NULL DEFAULT 1,
    uid_ciphertext VARBINARY(512) NOT NULL,
    uid_lookup BINARY(32) NOT NULL,
    email_ciphertext VARBINARY(1024) NOT NULL,
    created_at TIMESTAMP(6) NOT NULL DEFAULT CURRENT_TIMESTAMP(6),
    updated_at TIMESTAMP(6) NOT NULL DEFAULT CURRENT_TIMESTAMP(6) ON UPDATE CURRENT_TIMESTAMP(6),
    PRIMARY KEY (id),
    UNIQUE KEY uq_admin_identity_uid_lookup (uid_lookup),
    CONSTRAINT chk_admin_identity_singleton CHECK (id = 1),
    CONSTRAINT chk_admin_identity_credential_epoch CHECK (credential_epoch >= 1)
) ENGINE=InnoDB DEFAULT CHARACTER SET utf8mb4 COLLATE utf8mb4_0900_ai_ci;

CREATE TABLE IF NOT EXISTS admin_totp (
    admin_identity_id TINYINT UNSIGNED NOT NULL,
    secret_ciphertext VARBINARY(512) NOT NULL,
    rotated_at TIMESTAMP(6) NOT NULL DEFAULT CURRENT_TIMESTAMP(6),
    PRIMARY KEY (admin_identity_id),
    CONSTRAINT fk_admin_totp_identity
        FOREIGN KEY (admin_identity_id) REFERENCES admin_identity (id)
        ON UPDATE RESTRICT ON DELETE RESTRICT
) ENGINE=InnoDB DEFAULT CHARACTER SET utf8mb4 COLLATE utf8mb4_0900_ai_ci;

CREATE TABLE IF NOT EXISTS admin_recovery_codes (
    id BIGINT UNSIGNED NOT NULL AUTO_INCREMENT,
    admin_identity_id TINYINT UNSIGNED NOT NULL,
    code_hash BINARY(32) NOT NULL,
    created_at TIMESTAMP(6) NOT NULL DEFAULT CURRENT_TIMESTAMP(6),
    used_at TIMESTAMP(6) NULL,
    invalidated_at TIMESTAMP(6) NULL,
    PRIMARY KEY (id),
    UNIQUE KEY uq_admin_recovery_codes_hash (code_hash),
    KEY idx_admin_recovery_codes_identity (admin_identity_id),
    CONSTRAINT fk_admin_recovery_codes_identity
        FOREIGN KEY (admin_identity_id) REFERENCES admin_identity (id)
        ON UPDATE RESTRICT ON DELETE RESTRICT,
    CONSTRAINT chk_admin_recovery_codes_terminal_state
        CHECK (used_at IS NULL OR invalidated_at IS NULL)
) ENGINE=InnoDB DEFAULT CHARACTER SET utf8mb4 COLLATE utf8mb4_0900_ai_ci;

CREATE TABLE IF NOT EXISTS site_sessions (
    id BIGINT UNSIGNED NOT NULL AUTO_INCREMENT,
    account_id BIGINT UNSIGNED NULL,
    admin_identity_id TINYINT UNSIGNED NULL,
    token_hash BINARY(32) NOT NULL,
    credential_epoch BIGINT UNSIGNED NOT NULL,
    created_at TIMESTAMP(6) NOT NULL DEFAULT CURRENT_TIMESTAMP(6),
    expires_at TIMESTAMP(6) NOT NULL,
    revoked_at TIMESTAMP(6) NULL,
    totp_verified_at TIMESTAMP(6) NULL,
    PRIMARY KEY (id),
    UNIQUE KEY uq_site_sessions_token_hash (token_hash),
    KEY idx_site_sessions_account (account_id),
    KEY idx_site_sessions_admin_identity (admin_identity_id),
    CONSTRAINT fk_site_sessions_account
        FOREIGN KEY (account_id) REFERENCES streamer_accounts (id)
        ON UPDATE RESTRICT ON DELETE RESTRICT,
    CONSTRAINT fk_site_sessions_admin_identity
        FOREIGN KEY (admin_identity_id) REFERENCES admin_identity (id)
        ON UPDATE RESTRICT ON DELETE RESTRICT,
    CONSTRAINT chk_site_sessions_one_principal CHECK (
        (account_id IS NOT NULL) + (admin_identity_id IS NOT NULL) = 1
    ),
    CONSTRAINT chk_site_sessions_credential_epoch CHECK (credential_epoch >= 1),
    CONSTRAINT chk_site_sessions_expiry CHECK (expires_at > created_at)
) ENGINE=InnoDB DEFAULT CHARACTER SET utf8mb4 COLLATE utf8mb4_0900_ai_ci;

CREATE TABLE IF NOT EXISTS invitation_quotas (
    account_id BIGINT UNSIGNED NOT NULL,
    remaining_quota BIGINT UNSIGNED NOT NULL DEFAULT 0,
    updated_at TIMESTAMP(6) NOT NULL DEFAULT CURRENT_TIMESTAMP(6) ON UPDATE CURRENT_TIMESTAMP(6),
    PRIMARY KEY (account_id),
    CONSTRAINT fk_invitation_quotas_account
        FOREIGN KEY (account_id) REFERENCES streamer_accounts (id)
        ON UPDATE RESTRICT ON DELETE RESTRICT,
    CONSTRAINT chk_invitation_quotas_nonnegative CHECK (remaining_quota >= 0)
) ENGINE=InnoDB DEFAULT CHARACTER SET utf8mb4 COLLATE utf8mb4_0900_ai_ci;

CREATE TABLE IF NOT EXISTS invitations (
    id BIGINT UNSIGNED NOT NULL AUTO_INCREMENT,
    code_hash BINARY(32) NOT NULL,
    code_hint CHAR(4) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
    creator_account_id BIGINT UNSIGNED NULL,
    creator_admin_identity_id TINYINT UNSIGNED NULL,
    invited_account_id BIGINT UNSIGNED NULL,
    status VARCHAR(16) CHARACTER SET ascii COLLATE ascii_bin NOT NULL DEFAULT 'active',
    created_at TIMESTAMP(6) NOT NULL DEFAULT CURRENT_TIMESTAMP(6),
    expires_at TIMESTAMP(6) NOT NULL,
    revoked_at TIMESTAMP(6) NULL,
    used_at TIMESTAMP(6) NULL,
    PRIMARY KEY (id),
    UNIQUE KEY uq_invitations_code_hash (code_hash),
    KEY idx_invitations_creator_account (creator_account_id),
    KEY idx_invitations_creator_admin (creator_admin_identity_id),
    KEY idx_invitations_invited_account (invited_account_id),
    CONSTRAINT fk_invitations_creator_account
        FOREIGN KEY (creator_account_id) REFERENCES streamer_accounts (id)
        ON UPDATE RESTRICT ON DELETE RESTRICT,
    CONSTRAINT fk_invitations_creator_admin
        FOREIGN KEY (creator_admin_identity_id) REFERENCES admin_identity (id)
        ON UPDATE RESTRICT ON DELETE RESTRICT,
    CONSTRAINT fk_invitations_invited_account
        FOREIGN KEY (invited_account_id) REFERENCES streamer_accounts (id)
        ON UPDATE RESTRICT ON DELETE RESTRICT,
    CONSTRAINT chk_invitations_one_creator CHECK (
        (creator_account_id IS NOT NULL) + (creator_admin_identity_id IS NOT NULL) = 1
    ),
    CONSTRAINT chk_invitations_status CHECK (
        status IN ('active', 'revoked', 'expired', 'used')
    ),
    CONSTRAINT chk_invitations_expiry CHECK (expires_at > created_at),
    CONSTRAINT chk_invitations_terminal_state CHECK (
        (status = 'active' AND revoked_at IS NULL AND used_at IS NULL AND invited_account_id IS NULL)
        OR (status = 'revoked' AND revoked_at IS NOT NULL AND used_at IS NULL AND invited_account_id IS NULL)
        OR (status = 'expired' AND revoked_at IS NULL AND used_at IS NULL AND invited_account_id IS NULL)
        OR (status = 'used' AND revoked_at IS NULL AND used_at IS NOT NULL AND invited_account_id IS NOT NULL)
    )
) ENGINE=InnoDB DEFAULT CHARACTER SET utf8mb4 COLLATE utf8mb4_0900_ai_ci;

CREATE TABLE IF NOT EXISTS invitation_quota_events (
    id BIGINT UNSIGNED NOT NULL AUTO_INCREMENT,
    account_id BIGINT UNSIGNED NOT NULL,
    invitation_id BIGINT UNSIGNED NULL,
    actor_admin_identity_id TINYINT UNSIGNED NULL,
    quota_delta BIGINT NOT NULL,
    quota_after BIGINT UNSIGNED NOT NULL,
    reason VARCHAR(64) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
    created_at TIMESTAMP(6) NOT NULL DEFAULT CURRENT_TIMESTAMP(6),
    PRIMARY KEY (id),
    KEY idx_invitation_quota_events_account (account_id),
    KEY idx_invitation_quota_events_invitation (invitation_id),
    KEY idx_invitation_quota_events_admin (actor_admin_identity_id),
    CONSTRAINT fk_invitation_quota_events_account
        FOREIGN KEY (account_id) REFERENCES streamer_accounts (id)
        ON UPDATE RESTRICT ON DELETE RESTRICT,
    CONSTRAINT fk_invitation_quota_events_invitation
        FOREIGN KEY (invitation_id) REFERENCES invitations (id)
        ON UPDATE RESTRICT ON DELETE RESTRICT,
    CONSTRAINT fk_invitation_quota_events_admin
        FOREIGN KEY (actor_admin_identity_id) REFERENCES admin_identity (id)
        ON UPDATE RESTRICT ON DELETE RESTRICT,
    CONSTRAINT chk_invitation_quota_events_nonnegative CHECK (quota_after >= 0),
    CONSTRAINT chk_invitation_quota_events_nonzero_delta CHECK (quota_delta <> 0)
) ENGINE=InnoDB DEFAULT CHARACTER SET utf8mb4 COLLATE utf8mb4_0900_ai_ci;

CREATE TABLE IF NOT EXISTS audit_events (
    id BIGINT UNSIGNED NOT NULL AUTO_INCREMENT,
    event_type VARCHAR(64) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
    actor_account_id BIGINT UNSIGNED NULL,
    actor_admin_identity_id TINYINT UNSIGNED NULL,
    target_account_id BIGINT UNSIGNED NULL,
    event_data JSON NOT NULL,
    created_at TIMESTAMP(6) NOT NULL DEFAULT CURRENT_TIMESTAMP(6),
    PRIMARY KEY (id),
    KEY idx_audit_events_actor_account (actor_account_id),
    KEY idx_audit_events_actor_admin (actor_admin_identity_id),
    KEY idx_audit_events_target_account (target_account_id),
    KEY idx_audit_events_type_created (event_type, created_at),
    CONSTRAINT fk_audit_events_actor_account
        FOREIGN KEY (actor_account_id) REFERENCES streamer_accounts (id)
        ON UPDATE RESTRICT ON DELETE RESTRICT,
    CONSTRAINT fk_audit_events_actor_admin
        FOREIGN KEY (actor_admin_identity_id) REFERENCES admin_identity (id)
        ON UPDATE RESTRICT ON DELETE RESTRICT,
    CONSTRAINT fk_audit_events_target_account
        FOREIGN KEY (target_account_id) REFERENCES streamer_accounts (id)
        ON UPDATE RESTRICT ON DELETE RESTRICT
) ENGINE=InnoDB DEFAULT CHARACTER SET utf8mb4 COLLATE utf8mb4_0900_ai_ci;

DROP TRIGGER IF EXISTS trg_audit_events_no_update;

CREATE TRIGGER trg_audit_events_no_update
BEFORE UPDATE ON audit_events
FOR EACH ROW
SIGNAL SQLSTATE '45000' SET MESSAGE_TEXT = 'audit_events are append-only';

DROP TRIGGER IF EXISTS trg_audit_events_no_delete;

CREATE TRIGGER trg_audit_events_no_delete
BEFORE DELETE ON audit_events
FOR EACH ROW
SIGNAL SQLSTATE '45000' SET MESSAGE_TEXT = 'audit_events are append-only';
