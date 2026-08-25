# Hosted service operations

Operate one invitation-only Hong Kong Linux host. Do not proxy through the domestic personal-ICP node or the Shanghai update mirror. Public ingress is 80/443 only. The application listens on loopback `127.0.0.1:12500`. Nginx must not proxy `/healthz` or `/internal/metrics`.

Record every production change with the image digest, migration head, backup object, checksum, health output, smoke-test IDs, and previous digest before switching traffic.

## Provisioning

1. Create the Hong Kong Lighthouse from the approved image. Do not reuse the Shanghai or Beijing instances.
2. Install Docker Compose 2.33.1+, Nginx, `age`, `zstd`, `coscli`, and `openssl`.
3. Create `/opt/gift-panel-hosted/releases`, `/opt/gift-panel-hosted/current`, `/etc/gift-panel-hosted`, `/var/lib/gift-panel-hosted-backup`, `/var/lib/gift-panel-hosted-log-archive`, and `/var/log/gift-panel-hosted`.
4. Copy `deploy/hosted/env.example` to `/etc/gift-panel-hosted/env`. Fill host-specific public values only. Secret variables must point at root-managed files under `/etc/gift-panel-hosted/secrets/`.
5. Enable `gift-panel-hosted.service`, `backup.timer`, and `archive-logs.timer`.

## DNS and TLS

Point the independent online domain at this host. Render `deploy/hosted/nginx.conf.template` with `ONLINE_DOMAIN`, `TLS_CERTIFICATE`, and `TLS_CERTIFICATE_KEY`. Require TLS 1.2/1.3. Keep HSTS without `includeSubDomains` until every subdomain is HTTPS. Renew certificates at least 21 days before expiry. `deploy/hosted/health-check.sh` alarm code 14 tracks that threshold.

## Firewall

Allow 80 and 443 from the public internet. Deny 12500, 3306, Docker socket, `/healthz`, and `/internal/metrics` from non-loopback sources. Confirm `curl --fail --silent --show-error http://127.0.0.1:12500/healthz` succeeds on the host and that the public hostname returns 404 for those paths.

## Secrets file modes

Install secret files as `root:root` mode `0600`. The environment file is `root:root` mode `0600`. Secret directories are `0700`. Never copy secret values into Git, image layers, command output, tickets, or this runbook.

## Administrator initialization

From a private root shell on the host, load the deployment environment and run the image entrypoint in the running app container once:

```bash
set -a
. /etc/gift-panel-hosted/env
set +a
cd /opt/gift-panel-hosted/current/deploy/hosted
docker compose exec app /usr/local/bin/hosted-entrypoint admin init --email <recovery-email>
```

Capture the TOTP URI, recovery package password, and confirmation token privately over an approved offline channel. Store the recovery archive offline and enroll the TOTP URI before confirming initialization. In the same private root shell, read the CSRF token, confirmation token, and current TOTP code without placing their values in command history. Do not paste any of these secrets into shell assignments or command arguments.

```bash
IFS= read -r HOSTED_ADMIN_CSRF_TOKEN < /etc/gift-panel-hosted/secrets/admin-csrf-token
read -r -s -p 'Confirmation token: ' HANDOFF_TOKEN
printf '\n'
read -r -s -p 'Current TOTP code: ' TOTP_CODE
printf '\n'
printf '{"handoffToken":"%s","totp":"%s"}' "$HANDOFF_TOKEN" "$TOTP_CODE" |
  curl --fail --silent --show-error \
    --request POST \
    --header "Origin: $HOSTED_ADMIN_ALLOWED_ORIGIN" \
    --header "X-CSRF-Token: $HOSTED_ADMIN_CSRF_TOKEN" \
    --header 'Content-Type: application/json' \
    --data-binary @- \
    http://127.0.0.1:12500/api/admin/recovery/confirm
unset HANDOFF_TOKEN TOTP_CODE HOSTED_ADMIN_CSRF_TOKEN
```

Successful confirmation returns HTTP 204 with no response body. Destroy the terminal scrollback and close the private shell. Do not log the URI, password, confirmation token, TOTP code, or CSRF token.

## Bilibili service credential

Bind the shared Bilibili service credential through the admin challenge/replace routes only. Rotate by completing a fresh challenge and replace. If the egress breaker opens, follow the Bilibili breaker procedure; do not retry from the host.

## Deploy

Build with `npm run build:hosted-server` from a reviewed commit. Record the image digest and previous digest. Copy the immutable release tree to `/opt/gift-panel-hosted/releases/<digest>/`. Render Compose with `/etc/gift-panel-hosted/env`. Run `docker compose -f deploy/hosted/docker-compose.yml config --quiet`, then `systemctl start gift-panel-hosted.service`. Do not use `latest` tags.

## Database migration

Schema migrations run on process start through the embedded SQL files. Record the migration head from `SELECT version FROM schema_migrations ORDER BY version`. Account configuration migrations apply only after the owning session ends. Rollback never reverses an applied schema destructively. If a release cannot start, restore the previous application image and keep the applied schema, or follow Backup restore when a documented restore decision is approved.

### Administrator invitation compatibility gate

Migration `0011_recoverable_admin_invitations` adds nullable invitation ciphertext but cannot reconstruct full codes for rows created before that migration. A pre-migration invitation can therefore remain `active` while `code_ciphertext` is NULL. Do not assume schema success means every active administrator invitation is recoverable: the administrator inventory may fail closed when it encounters such a row.

Before enabling the administrator invitation inventory on an existing database, record only aggregate results and require the legacy-active count to be zero:

```sql
SELECT status,
       COUNT(*) AS total,
       SUM(code_ciphertext IS NULL) AS missing_ciphertext
FROM invitations
WHERE creator_admin_identity_id IS NOT NULL
GROUP BY status
ORDER BY status;
```

For a service that has already admitted users, preserve invitation history and implement a reviewed compatibility or retirement path; do not delete records merely to make the page load. For an unopened pilot containing confirmed test data only, cleanup is allowed after explicit approval and a fresh encrypted backup. Inventory creator types first, remove only the approved administrator test scope, and delete foreign-key dependants in the same transaction:

```sql
START TRANSACTION;
SELECT COUNT(*)
FROM invitations
WHERE creator_admin_identity_id IS NOT NULL;

DELETE quota_event
FROM invitation_quota_events AS quota_event
JOIN invitations AS invitation ON invitation.id = quota_event.invitation_id
WHERE invitation.creator_admin_identity_id IS NOT NULL;

DELETE FROM invitations
WHERE creator_admin_identity_id IS NOT NULL;
COMMIT;

SELECT COUNT(*) FROM invitations;
```

Record the backup completion marker, affected-row counts, post-cleanup aggregate count, application image digest, and loopback health result. Never print full codes, ciphertext, administrator identities, account identifiers, or secret-file contents. Creating a new invitation after cleanup is the functional canary; logging out and back in must still show the newly created row.

## Canary check

After deploy, run `deploy/hosted/health-check.sh` on the host. Confirm loopback `http://127.0.0.1:12500/healthz` returns `{"status":"ok"}`. Confirm loopback `http://127.0.0.1:12500/internal/metrics` returns identity-free gauges. Record health output and smoke-test IDs for one admin login, one invitation redeem, one runtime room switch, and one OBS event stream. Public `/healthz` and `/internal/metrics` must remain unrouted.

## Rollback

Switch `/opt/gift-panel-hosted/current` back to the previous digest and start the previous immutable image. Keep the current schema. Do not run down-migrations. If data is wrong, use Backup restore rather than reversing DDL.

## Application key rotation

Generate a new application CSRF/session material offline. Replace the corresponding secret file atomically (`install` to a temp name, `mv`). Restart the app unit. Invalidate existing browser sessions. Record the rotation without the secret value.

## HMAC key rotation

Replace `HOSTED_HMAC_KEY_FILE` the same way. Restart the app. Existing HMAC-backed tokens fail closed until callers obtain new tokens.

## Encryption key rotation

Replace `HOSTED_ENCRYPTION_KEY_FILE` only with a planned re-encryption procedure. Do not delete the previous key until every stored ciphertext is readable with the new key. If re-encryption is not ready, abort and restore the previous file.

## SMTP rotation

Replace `HOSTED_SMTP_PASSWORD_FILE` and update non-secret SMTP host values in `/etc/gift-panel-hosted/env`. Send one recovery-mail canary to the administrator mailbox. Do not print the password.

## COS rotation

Replace the host `coscli` configuration used by backup and log archive. Confirm `HOSTED_COS_REGION=ap-hongkong`. Production credentials must not delete historical backups. Run one backup dry path only after the new config can upload with `--forbid-overwrite`.

## TLS rotation

Install the new certificate and key, `nginx -t`, then reload Nginx. Keep the previous certificate files until health-check code 14 is green. Update `HOSTED_TLS_CERTIFICATE` for monitoring.

## Backup restore

Use `deploy/hosted/restore-drill.sh` against a disposable Compose project. Record the backup object and checksum before restore. Restore never targets the production MySQL volume in place. After a production restore decision, start the previous or current application image on the restored data and re-run Canary check.

## Service-account compromise

Disable the leaked CAM/COS/SMTP/host key immediately. Rotate every secret file on this host. Treat backups written after the leak as untrusted until checksums and object names are reviewed. Rebuild a replacement host if the operating system account is involved. Follow Server decommission for the compromised machine.

## Bilibili breaker

If `hosted_bilibili_breaker_open` is 1, stop adding rooms and do not restart the process to clear the breaker. Wait for the two-minute hold and three successful half-open probes. Capture aggregate reconnect/risk/rate-limit/failure gauges only. Do not capture room numbers or cookies.

## Disk full

If health-check returns 11, stop invitation expansion, pause non-essential log copies, and free space from `/tmp` and completed restore drills only. Do not delete backup or archive objects. Restore disk headroom before enabling timers.

## Account disable

Disable the account in admin. Runtime must drop that account's leases. Do not delete historical rows to hide an incident. Record the operator action without copying configuration JSON.

## Server decommission

Disable public DNS, revoke certificates, stop Compose, stop timers, and shut the instance. Leave COS historical backups under lifecycle rules. Destroy local secret files with `shred` or equivalent. Do not reuse the disk image for another environment.
