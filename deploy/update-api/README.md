# Gift Panel Update API deployment

This deployment serves private COS release metadata through the API only. Keep the COS bucket private; do not enable public read or configure a CDN. Task 4 production gate: COS Versioning state must be `Disabled`; `Suspended` is not acceptable. A bucket that was previously enabled or suspended cannot satisfy this gate because Task 4's overwrite-forbidden immutable writes are ineffective there. Provision a new never-versioned production bucket, or adopt and verify a redesigned immutable mechanism before using that bucket for releases. The API issues short-lived signed download URLs, and neither Nginx access logs nor this runbook records their query strings.

## Required names

GitHub Actions variables: `UPDATE_API_BASE_URL`, `COS_BUCKET`, `COS_REGION`, `EVSIGN_EXPECTED_SUBJECT`, `UPDATE_PUBLISHER_TOOL_SHA`.

GitHub Actions secrets: `COS_RELEASE_SECRET_ID`, `COS_RELEASE_SECRET_KEY`.

Store these release variables and secrets only in the protected GitHub Environment `release`, with its approval and branch rules enabled. `UPDATE_PUBLISHER_TOOL_SHA` is not secret, but it is a production trust decision: it must be an exact 40-hex commit SHA, never a tag, branch, shortened SHA, repository-level override, or workflow-dispatch input. The workflow validates the pin before checkout, checks the publisher tooling out separately from the requested release tag, verifies the resolved commit, and runs `updateapi/cmd/publish` only from that checkout. The requested tag checkout remains the source of release artifacts and metadata.

Server environment variables: `UPDATE_API_LISTEN`, `COS_BUCKET`, `COS_REGION`, `COS_SECRET_ID`, `COS_SECRET_KEY`. The channel object is fixed in the binary as `channels/stable/latest.json` and is not configurable.

Rendering variables: `PUBLIC_DOMAIN`, `ICP_NUMBER`, `TLS_CERT_PATH`, `TLS_KEY_PATH`.

## Rotate the publisher tool pin

To update the publisher without weakening repair safety, review the candidate commit and its complete diff, confirm that its `updateapi` race tests cover immutable release objects, stable-last promotion, rollback, and monotonic SemVer behavior, and obtain approval under the release change-control process. Copy the candidate's full 40-character commit SHA from the reviewed repository; do not copy a branch or tag name.

After approval, update the environment variable `UPDATE_PUBLISHER_TOOL_SHA` in the protected GitHub Environment `release`. Keep the COS and signing credentials in that same Environment, and never expose the pin as a workflow-dispatch input. Rerun the Release workflow under Environment approval and verify that `Validate update publisher tool commit` succeeds and `Verify update publisher tool checkout` resolves the exact approved SHA before allowing the COS mirror step. If validation or checkout verification fails, restore the last reviewed pin; do not bypass either gate.

## Install

Create the dedicated service account before installing the binary:

```sh
sudo useradd --system --user-group --home-dir /nonexistent --shell /usr/sbin/nologin gift-panel-update
```

Build the Linux binary from a trusted checkout and install it:

```sh
npm run build:update-api
sudo install -d -o root -g root -m 0755 /opt/gift-panel-update-api/releases /opt/gift-panel-update-api/www /var/www/acme
sudo install -d -o root -g root -m 0755 /opt/gift-panel-update-api/releases/RELEASE_ID
sudo install -o root -g gift-panel-update -m 0755 dist/gift-panel-update-api-linux-amd64 /opt/gift-panel-update-api/releases/RELEASE_ID/gift-panel-update-api
sudo ln -sfn /opt/gift-panel-update-api/releases/RELEASE_ID /opt/gift-panel-update-api/current
```

Create `/etc/gift-panel-update-api.env` from `gift-panel-update-api.env.example`, populate it through an approved secret channel, then install it root-owned and mode `0600`. Never commit that file. The systemd unit forces `UPDATE_API_LISTEN=127.0.0.1:12450`; do not add that variable to the environment file. The Go server rejects non-loopback listeners as a final boundary.

```sh
sudo install -o root -g root -m 0600 /secure/gift-panel-update-api.env /etc/gift-panel-update-api.env
```

Render the public page and Nginx configuration only on the server, after exporting the four rendering variables:

```sh
envsubst '${ICP_NUMBER}' < deploy/update-api/index.html.template | sudo tee /opt/gift-panel-update-api/www/index.html >/dev/null
envsubst '${PUBLIC_DOMAIN} ${TLS_CERT_PATH} ${TLS_KEY_PATH}' < deploy/update-api/nginx.conf.template | sudo tee /etc/nginx/conf.d/gift-panel-update-api.conf >/dev/null
sudo install -o root -g root -m 0644 deploy/update-api/gift-panel-update-api.service /etc/systemd/system/gift-panel-update-api.service
sudo install -o root -g root -m 0644 deploy/update-api/logrotate.conf /etc/logrotate.d/gift-panel-update-api
sudo install -d -o root -g root -m 0755 /etc/systemd/journald@gift-panel-update-api.conf.d
sudo install -o root -g root -m 0644 deploy/update-api/journald.conf /etc/systemd/journald@gift-panel-update-api.conf.d/retention.conf
sudo systemctl daemon-reload
sudo systemd-analyze verify /etc/systemd/system/gift-panel-update-api.service
sudo nginx -t
sudo systemctl enable --now gift-panel-update-api.service
sudo systemctl reload nginx.service
```

Nginx uses a dedicated log format that contains `$uri`, never `$args` or `$request_uri`, so signed COS query strings are not recorded. Rotate it daily and retain seven compressed archives:

```sh
sudo logrotate -vf /etc/logrotate.d/gift-panel-update-api
```

The service writes to its dedicated journal namespace. Inspect it with `journalctl --namespace=gift-panel-update-api -u gift-panel-update-api.service`; the namespace configuration caps persistent and volatile storage at 64 MiB and retains entries for at most seven days.

## Verify and operate

Run health checks locally from the server and only call public API routes over HTTPS:

```sh
curl --fail --silent --show-error http://127.0.0.1:12450/healthz | grep -Fx 'ok'
curl --fail --silent --show-error https://PUBLIC_DOMAIN/api/v1/releases/latest
curl --fail --silent --show-error https://PUBLIC_DOMAIN/api/v1/changelog
```

Before changing the stable channel, back up the current private COS object `channels/stable/latest.json` to a dated private key such as `channels/stable/backups/DATE/latest.json` using the approved COS operator tooling. To roll back, restore that verified backup to `channels/stable/latest.json`; do not overwrite immutable `releases/` objects. Restart the service only if its credentials or binary changed.

Automated publish and repair are monotonic by canonical SemVer. The publisher mirrors and verifies immutable `releases/TAG/` objects first, then advances `channels/stable/latest.json` only when `TAG` is greater than the currently validated stable tag. An equal or older repair succeeds with `stable unchanged`; it must never be used to downgrade latest. An intentional rollback is a separate operator action: restore a reviewed backup as described above and verify both public API routes before allowing another automated publish.

Rotate COS credentials by creating a replacement least-privilege key, updating `/etc/gift-panel-update-api.env` with root ownership and mode `0600`, running `sudo systemctl restart gift-panel-update-api.service`, verifying the local health and HTTPS API checks above, then revoking the old key. Do not copy credentials, signed URLs, or their query strings into tickets, shell history, logs, commits, or chat.

For an Nginx rollback, restore the last known-good `/etc/nginx/conf.d/gift-panel-update-api.conf`, run `sudo nginx -t`, and then `sudo systemctl reload nginx.service`. To roll back the API binary, repoint `/opt/gift-panel-update-api/current` to the previous release directory and restart `gift-panel-update-api.service` after the same verification checks.

## Release mirror (separate service)

The mirror is separate from the update API: it uses a no-login account, its own systemd state directory and journal namespace, and opens no public socket. Its CAM identity is scoped read/write: only Head/Get/Put for `releases/*` and `channels/stable/latest.json`; no Delete, list, bucket configuration, or other prefixes.

Set the reviewed inputs independently of the build output. Deployment builds archive the exact clean Git commit before compiling; `GIFT_PANEL_LOCAL_BUILD=1` writes only `dist/local/` and must never be installed.

```sh
set -euo pipefail
RELEASE_ID="${REVIEWED_COMMIT:?set the reviewed 40-hex commit}"
REVIEWED_SHA256="${REVIEWED_SHA256:?set the independently reviewed mirror SHA-256}"
test "$RELEASE_ID" = "$(git rev-parse HEAD)"
npm run build:update-api
test "$RELEASE_ID" = "$(dist/gift-panel-release-mirror-linux-amd64 --build-commit)"
printf '%s  %s\n' "$REVIEWED_SHA256" 'dist/gift-panel-release-mirror-linux-amd64' | sha256sum -c -
printf '%s\n%s\n' "$RELEASE_ID" "$REVIEWED_SHA256" > gift-panel-release-mirror.reviewed
test "$(wc -l < gift-panel-release-mirror.reviewed)" -eq 2
```

Transfer both the verified normal artifact and the two-line `gift-panel-release-mirror.reviewed` sidecar by the approved secure channel. On Lighthouse, redefine evidence from the transferred sidecar rather than relying on local-shell variables, then verify the installed directory, binary identity, checksum, and `current` target before any systemd start:

```sh
set -euo pipefail
test "$(wc -l < gift-panel-release-mirror.reviewed)" -eq 2
RELEASE_ID=$(sed -n '1p' gift-panel-release-mirror.reviewed)
REVIEWED_SHA256=$(sed -n '2p' gift-panel-release-mirror.reviewed)
printf '%s\n' "$RELEASE_ID" | grep -Eq '^[0-9a-f]{40}$'
printf '%s\n' "$REVIEWED_SHA256" | grep -Eq '^[0-9a-f]{64}$'
if ! getent passwd gift-panel-mirror >/dev/null; then
  if getent group gift-panel-mirror >/dev/null; then exit 1; fi
  sudo useradd --system --user-group --home-dir /nonexistent --shell /usr/sbin/nologin gift-panel-mirror
fi
ACCOUNT_RECORD=$(getent passwd gift-panel-mirror)
ACCOUNT_GID=$(printf '%s\n' "$ACCOUNT_RECORD" | cut -d: -f4)
test "$(printf '%s\n' "$ACCOUNT_RECORD" | cut -d: -f1)" = gift-panel-mirror
test "$(printf '%s\n' "$ACCOUNT_RECORD" | cut -d: -f6)" = /nonexistent
test "$(printf '%s\n' "$ACCOUNT_RECORD" | cut -d: -f7)" = /usr/sbin/nologin
GROUP_RECORD=$(getent group "$ACCOUNT_GID")
test "$(printf '%s\n' "$GROUP_RECORD" | cut -d: -f1)" = gift-panel-mirror
test "$(printf '%s\n' "$GROUP_RECORD" | cut -d: -f3)" = "$ACCOUNT_GID"
test "$(id -gn gift-panel-mirror)" = gift-panel-mirror
sudo install -d -o root -g root -m 0755 /opt/gift-panel-release-mirror/releases/"$RELEASE_ID"
sudo install -o root -g root -m 0755 gift-panel-release-mirror-linux-amd64 /opt/gift-panel-release-mirror/releases/"$RELEASE_ID"/gift-panel-release-mirror
printf '%s  %s\n' "$REVIEWED_SHA256" /opt/gift-panel-release-mirror/releases/"$RELEASE_ID"/gift-panel-release-mirror | sha256sum -c -
test "$RELEASE_ID" = "$(/opt/gift-panel-release-mirror/releases/"$RELEASE_ID"/gift-panel-release-mirror --build-commit)"
sudo ln -sfn /opt/gift-panel-release-mirror/releases/"$RELEASE_ID" /opt/gift-panel-release-mirror/current
test "$(readlink -f /opt/gift-panel-release-mirror/current/gift-panel-release-mirror)" = "/opt/gift-panel-release-mirror/releases/$RELEASE_ID/gift-panel-release-mirror"
test "$RELEASE_ID" = "$(/opt/gift-panel-release-mirror/current/gift-panel-release-mirror --build-commit)"
```

Install the separate root-owned `0600` environment and units. It contains only the mirror scoped CAM credentials and never reuses `/etc/gift-panel-update-api.env`:

```sh
set -euo pipefail
sudo install -o root -g root -m 0600 /secure/gift-panel-release-mirror.env /etc/gift-panel-release-mirror.env
sudo install -o root -g root -m 0644 deploy/update-api/gift-panel-release-mirror.service /etc/systemd/system/gift-panel-release-mirror.service
sudo install -o root -g root -m 0644 deploy/update-api/gift-panel-release-mirror.timer /etc/systemd/system/gift-panel-release-mirror.timer
sudo install -d -o root -g root -m 0755 /etc/systemd/journald@gift-panel-release-mirror.conf.d
sudo install -o root -g root -m 0644 deploy/update-api/journald.conf /etc/systemd/journald@gift-panel-release-mirror.conf.d/retention.conf
sudo systemctl daemon-reload
sudo systemd-analyze verify /etc/systemd/system/gift-panel-release-mirror.service /etc/systemd/system/gift-panel-release-mirror.timer
```

Do not run the binary manually on a fresh host: systemd must create `StateDirectory` first. Before enabling the timer, use a temporary dry-run drop-in, inspect the completed invocation, remove the drop-in, then run one normal oneshot. The cleanup below is deliberately failure-safe: an interrupted or failed validation cannot reach normal publication, and it removes only the exact resolved drop-in path.

```sh
set -euo pipefail
DROPIN=/run/systemd/system/gift-panel-release-mirror.service.d/dry-run.conf
DROPIN_DIR=$(dirname "$DROPIN")
test "$DROPIN_DIR" = /run/systemd/system/gift-panel-release-mirror.service.d
cleanup_dry_run() {
  cleanup_failed=0
  if test -e "$DROPIN"; then sudo rm -- "$DROPIN" || cleanup_failed=1; fi
  sudo systemctl daemon-reload || cleanup_failed=1
  test ! -e "$DROPIN" || cleanup_failed=1
  active_dropins=$(sudo systemctl show -p DropInPaths --value gift-panel-release-mirror.service)
  if test $? -ne 0; then
    cleanup_failed=1
  elif printf '%s\n' "$active_dropins" | grep -Fq -- "$DROPIN"; then
    cleanup_failed=1
  fi
  return "$cleanup_failed"
}
finish_dry_run() {
  original_rc=$1
  trap - EXIT INT TERM
  set +e
  cleanup_dry_run
  cleanup_rc=$?
  set -e
  if test "$original_rc" -ne 0; then exit "$original_rc"; fi
  if test "$cleanup_rc" -ne 0; then exit 1; fi
  exit 0
}
on_exit() { finish_dry_run "$?"; }
on_int() { finish_dry_run 130; }
on_term() { finish_dry_run 143; }
trap on_exit EXIT
trap on_int INT
trap on_term TERM
sudo install -d -o root -g root -m 0755 "$DROPIN_DIR"
printf '[Service]\nExecStart=\nExecStart=/opt/gift-panel-release-mirror/current/gift-panel-release-mirror --dry-run\n' | sudo tee "$DROPIN" >/dev/null
sudo systemctl daemon-reload
sudo systemctl start gift-panel-release-mirror.service
test "$(sudo systemctl show -p Result --value gift-panel-release-mirror.service)" = success
sudo journalctl --namespace=gift-panel-release-mirror -u gift-panel-release-mirror.service --no-pager
set +e
cleanup_dry_run
cleanup_rc=$?
set -e
test "$cleanup_rc" -eq 0
trap - EXIT INT TERM
sudo systemctl start gift-panel-release-mirror.service
sudo systemctl enable --now gift-panel-release-mirror.timer
```

For rollback, quiesce both timer and oneshot before changing `current`. If the stable pointer also needs to move backward, stop here and use a separately confirmed Tencent COS console or approved operator action to restore the reviewed private `channels/stable/latest.json` backup while quiesced. do not re-enable timer while an intentionally older stable release would immediately be re-promoted. Do not delete immutable release objects. After that separate action, verify the domestic API's public metadata against approved rollback evidence without writing, printing, or logging its signed download URL. Lighthouse needs the Node.js runtime already used by the build verifier for this safe streaming check.

```sh
set -euo pipefail
sudo systemctl disable --now gift-panel-release-mirror.timer
sudo systemctl stop gift-panel-release-mirror.service
sudo systemctl is-active --quiet gift-panel-release-mirror.timer && exit 1
sudo systemctl is-active --quiet gift-panel-release-mirror.service && exit 1
sudo ln -sfn /opt/gift-panel-release-mirror/releases/PREVIOUS_RELEASE_ID /opt/gift-panel-release-mirror/current
PUBLIC_DOMAIN="${PUBLIC_DOMAIN:?set the domestic API domain}"
APPROVED_TAG="${APPROVED_TAG:?set the approved rollback tag}"
APPROVED_SHA256="${APPROVED_SHA256:?set the approved rollback SHA-256}"
APPROVED_SIZE="${APPROVED_SIZE:?set the approved rollback asset size}"
curl --fail --silent --show-error "https://$PUBLIC_DOMAIN/api/v1/releases/latest" | node -e '
const { readFileSync } = require("node:fs");
const [tag, sha256, expectedSize] = process.argv.slice(1);
try {
  const latest = JSON.parse(readFileSync(0, "utf8"));
  const expectedIntegerSize = Number(expectedSize);
  const assets = latest.assets;
  const asset = Array.isArray(assets) && assets.length === 1 ? assets[0] : null;
  if (!/^(?:0|[1-9][0-9]*)$/.test(expectedSize) || !Number.isSafeInteger(expectedIntegerSize) || latest.tag_name !== tag || !asset || asset.name !== "gift-panel-windows-x64.exe" || asset.digest !== "sha256:" + sha256 || !Number.isSafeInteger(asset.size) || asset.size !== expectedIntegerSize) process.exit(1);
  process.exit(0);
} catch { process.exit(1); }
' "$APPROVED_TAG" "$APPROVED_SHA256" "$APPROVED_SIZE"
if ! sudo jq -e --arg tag "$APPROVED_TAG" --arg sha "$APPROVED_SHA256" '.tag == $tag and .sha256 == $sha' /var/lib/gift-panel-release-mirror/state.json; then
  test "${ROLLBACK_STATE_POLICY:?set state-matches-approved or stable-pointer-restored-while-mirror-state-remains-newer}" = stable-pointer-restored-while-mirror-state-remains-newer
  printf 'rollback state intentionally differs: stable pointer restored while mirror state remains newer\n' | sudo systemd-cat -t gift-panel-release-mirror
fi
DROPIN=/run/systemd/system/gift-panel-release-mirror.service.d/dry-run.conf
DROPIN_DIR=$(dirname "$DROPIN")
test "$DROPIN_DIR" = /run/systemd/system/gift-panel-release-mirror.service.d
cleanup_dry_run() {
  cleanup_failed=0
  if test -e "$DROPIN"; then sudo rm -- "$DROPIN" || cleanup_failed=1; fi
  sudo systemctl daemon-reload || cleanup_failed=1
  test ! -e "$DROPIN" || cleanup_failed=1
  active_dropins=$(sudo systemctl show -p DropInPaths --value gift-panel-release-mirror.service)
  if test $? -ne 0; then
    cleanup_failed=1
  elif printf '%s\n' "$active_dropins" | grep -Fq -- "$DROPIN"; then
    cleanup_failed=1
  fi
  return "$cleanup_failed"
}
finish_dry_run() {
  original_rc=$1
  trap - EXIT INT TERM
  set +e
  cleanup_dry_run
  cleanup_rc=$?
  set -e
  if test "$original_rc" -ne 0; then exit "$original_rc"; fi
  if test "$cleanup_rc" -ne 0; then exit 1; fi
  exit 0
}
on_exit() { finish_dry_run "$?"; }
on_int() { finish_dry_run 130; }
on_term() { finish_dry_run 143; }
trap on_exit EXIT
trap on_int INT
trap on_term TERM
sudo install -d -o root -g root -m 0755 "$DROPIN_DIR"
printf '[Service]\nExecStart=\nExecStart=/opt/gift-panel-release-mirror/current/gift-panel-release-mirror --dry-run\n' | sudo tee "$DROPIN" >/dev/null
sudo systemctl daemon-reload
sudo systemctl start gift-panel-release-mirror.service
test "$(sudo systemctl show -p Result --value gift-panel-release-mirror.service)" = success
sudo journalctl --namespace=gift-panel-release-mirror -u gift-panel-release-mirror.service --no-pager
set +e
cleanup_dry_run
cleanup_rc=$?
set -e
test "$cleanup_rc" -eq 0
trap - EXIT INT TERM
# Only after this rollback dry-run succeeds may an operator decide whether to restart/re-enable.
```
