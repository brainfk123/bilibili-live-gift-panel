# Gift Panel Update API deployment

This deployment serves private COS release metadata through the API only. Keep the COS bucket private; do not enable public read or configure a CDN. Task 4 production gate: COS Versioning state must be `Disabled`; `Suspended` is not acceptable. A bucket that was previously enabled or suspended cannot satisfy this gate because Task 4's overwrite-forbidden immutable writes are ineffective there. Provision a new never-versioned production bucket, or adopt and verify a redesigned immutable mechanism before using that bucket for releases. The API issues short-lived signed download URLs, and neither Nginx access logs nor this runbook records their query strings.

## Required names

GitHub Actions variables: `UPDATE_API_BASE_URL`, `EVSIGN_CERT`, `EVSIGN_EXPECTED_SUBJECT`.

GitHub Actions secrets: `EVSIGN_KEY`, `EVSIGN_PASSWORD`.

Store these signing variables and secrets only in the protected GitHub Environment `release`, with its approval and branch rules enabled. The Release workflow uses the requested tag checkout for the update-module race test, build, signature, GitHub Release creation or complete-Release repair, and final asset validation. A validated GitHub Release is the workflow's terminal success condition; the workflow does not hold COS credentials or invoke the COS publisher.

Server environment variables: `UPDATE_API_LISTEN`, `COS_BUCKET`, `COS_REGION`, `COS_SECRET_ID`, `COS_SECRET_KEY`. The channel object is fixed in the binary as `channels/stable/latest.json` and is not configurable.

Mirror environment variables: `COS_BUCKET`, `COS_REGION`, `COS_SECRET_ID`, `COS_SECRET_KEY`. They belong only in the root-owned `/etc/gift-panel-release-mirror.env` on Lighthouse and must not be copied into GitHub.

Rendering variables: `PUBLIC_DOMAIN`, `ICP_NUMBER`, `TLS_CERT_PATH`, `TLS_KEY_PATH`.

The Lighthouse timer checks the public GitHub Release asynchronously every five minutes. GitHub publication never waits for the mirror. The oneshot validates all required public assets before constructing a COS client, preserves immutable release objects, and advances the stable pointer only after complete verification.

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

The mirror is separate from the update API: it uses a no-login account, its own systemd state directory and journal namespace, and opens no public socket. Give it the dedicated CAM identity `lighthouse-cos-publisher`; do not reuse the read-only update API identity or any GitHub publishing identity.

Before attaching the policy, use the Tencent CAM policy simulator to prove that only `name/cos:HeadObject`, `name/cos:GetObject`, and `name/cos:PutObject` are allowed for the immutable release prefix and stable pointer. This is Head/Get/Put only: no Delete, list, bucket configuration, or other prefixes. Another bucket and every other resource must also be denied. The production policy is:

```json
{
  "version": "2.0",
  "statement": [{
    "effect": "allow",
    "action": [
      "name/cos:HeadObject",
      "name/cos:GetObject",
      "name/cos:PutObject"
    ],
    "resource": [
      "qcs::cos:ap-shanghai:uid/1256302443:bilibili-live-gift-panel-1256302443/releases/*",
      "qcs::cos:ap-shanghai:uid/1256302443:bilibili-live-gift-panel-1256302443/channels/stable/latest.json"
    ]
  }]
}
```

Creating the identity, attaching or changing this policy, and creating a key are separate production mutations that require action-time approval. Keep the bucket private and never grant DeleteObject.

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

STOP: obtain separate action-time operator confirmation before production transfer or any install/quiesce action. Build approval is not transfer/install approval. After confirmation, proceed with the separately authorized transfer. Transfer both the verified normal artifact and the two-line `gift-panel-release-mirror.reviewed` sidecar by the approved secure channel.

Run this read-only-evidence plus quiesce preflight. It fails on every systemctl query, D-Bus, permission, command, unknown-state, active-state, or enabled-timer error. A fresh host is accepted only when both units report `LoadState=not-found`, `ActiveState=inactive`, and the timer's raw `UnitFileState` is empty or `not-found`; that raw value is normalized to the explicit state `not-found`.

```sh
set -euo pipefail
test "$(wc -l < gift-panel-release-mirror.reviewed)" -eq 2
RELEASE_ID=$(sed -n '1p' gift-panel-release-mirror.reviewed)
REVIEWED_SHA256=$(sed -n '2p' gift-panel-release-mirror.reviewed)
printf '%s\n' "$RELEASE_ID" | grep -Eq '^[0-9a-f]{40}$'
printf '%s\n' "$REVIEWED_SHA256" | grep -Eq '^[0-9a-f]{64}$'
mirror_systemctl_value() {
  property=$1 unit=$2
  value=$(sudo systemctl show --property="$property" --value "$unit") || return 1
  case "$value" in ''|*[!a-z-]*) return 1 ;; esac
  printf '%s\n' "$value"
}
mirror_timer_unit_file_state() {
  timer_load=$1
  raw=$(sudo systemctl show --property=UnitFileState --value gift-panel-release-mirror.timer) || return 1
  case "$timer_load:$raw" in
    loaded:disabled) printf '%s\n' disabled ;;
    not-found:|not-found:not-found) printf '%s\n' not-found ;;
    *) return 1 ;;
  esac
}
mirror_verify_quiesced() {
  timer_load=$(mirror_systemctl_value LoadState gift-panel-release-mirror.timer) || return 1
  timer_active=$(mirror_systemctl_value ActiveState gift-panel-release-mirror.timer) || return 1
  timer_unit_file=$(mirror_timer_unit_file_state "$timer_load") || return 1
  service_load=$(mirror_systemctl_value LoadState gift-panel-release-mirror.service) || return 1
  service_active=$(mirror_systemctl_value ActiveState gift-panel-release-mirror.service) || return 1
  case "$timer_load:$timer_active:$timer_unit_file" in loaded:inactive:disabled|not-found:inactive:not-found) ;; *) return 1 ;; esac
  case "$service_load:$service_active" in loaded:inactive|not-found:inactive) ;; *) return 1 ;; esac
}
mirror_quiesce() {
  timer_load=$(mirror_systemctl_value LoadState gift-panel-release-mirror.timer) || return 1
  service_load=$(mirror_systemctl_value LoadState gift-panel-release-mirror.service) || return 1
  case "$timer_load" in loaded) sudo systemctl disable --now gift-panel-release-mirror.timer || return 1 ;; not-found) ;; *) return 1 ;; esac
  case "$service_load" in loaded) sudo systemctl stop gift-panel-release-mirror.service || return 1 ;; not-found) ;; *) return 1 ;; esac
  mirror_verify_quiesced
}
mirror_quiesce
```

STOP: obtain separate action-time operator confirmation before service-user, version, and unit installation. The transfer/quiesce confirmation does not authorize account creation or writes under `/opt` or `/etc/systemd`.

After that confirmation, re-read the transferred evidence, re-issue and verify quiescence, then stage on the same filesystem. Never write into an existing version directory; an existing exact version is accepted only when its complete contents are byte-identical.

```sh
set -euo pipefail
test "$(wc -l < gift-panel-release-mirror.reviewed)" -eq 2
RELEASE_ID=$(sed -n '1p' gift-panel-release-mirror.reviewed)
REVIEWED_SHA256=$(sed -n '2p' gift-panel-release-mirror.reviewed)
printf '%s\n' "$RELEASE_ID" | grep -Eq '^[0-9a-f]{40}$'
printf '%s\n' "$REVIEWED_SHA256" | grep -Eq '^[0-9a-f]{64}$'
mirror_systemctl_value() {
  property=$1 unit=$2; value=$(sudo systemctl show --property="$property" --value "$unit") || return 1
  case "$value" in ''|*[!a-z-]*) return 1 ;; esac; printf '%s\n' "$value"
}
mirror_timer_unit_file_state() {
  timer_load=$1; raw=$(sudo systemctl show --property=UnitFileState --value gift-panel-release-mirror.timer) || return 1
  case "$timer_load:$raw" in loaded:disabled) printf '%s\n' disabled ;; not-found:|not-found:not-found) printf '%s\n' not-found ;; *) return 1 ;; esac
}
mirror_verify_quiesced() {
  timer_load=$(mirror_systemctl_value LoadState gift-panel-release-mirror.timer) || return 1
  timer_active=$(mirror_systemctl_value ActiveState gift-panel-release-mirror.timer) || return 1
  timer_unit_file=$(mirror_timer_unit_file_state "$timer_load") || return 1
  service_load=$(mirror_systemctl_value LoadState gift-panel-release-mirror.service) || return 1
  service_active=$(mirror_systemctl_value ActiveState gift-panel-release-mirror.service) || return 1
  case "$timer_load:$timer_active:$timer_unit_file" in loaded:inactive:disabled|not-found:inactive:not-found) ;; *) return 1 ;; esac
  case "$service_load:$service_active" in loaded:inactive|not-found:inactive) ;; *) return 1 ;; esac
}
mirror_quiesce() {
  timer_load=$(mirror_systemctl_value LoadState gift-panel-release-mirror.timer) || return 1
  service_load=$(mirror_systemctl_value LoadState gift-panel-release-mirror.service) || return 1
  case "$timer_load" in loaded) sudo systemctl disable --now gift-panel-release-mirror.timer || return 1 ;; not-found) ;; *) return 1 ;; esac
  case "$service_load" in loaded) sudo systemctl stop gift-panel-release-mirror.service || return 1 ;; not-found) ;; *) return 1 ;; esac
  mirror_verify_quiesced
}
mirror_quiesce
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
RELEASE_ROOT=/opt/gift-panel-release-mirror/releases
FINAL_RELEASE="$RELEASE_ROOT/$RELEASE_ID"
FINAL_BINARY="$FINAL_RELEASE/gift-panel-release-mirror"
sudo install -d -o root -g root -m 0755 "$RELEASE_ROOT"
STAGE_DIR=$(sudo mktemp -d "$RELEASE_ROOT/.stage-${RELEASE_ID}.XXXXXX")
STAGED_BINARY="$STAGE_DIR/gift-panel-release-mirror"
cleanup_stage() { if test -n "${STAGE_DIR:-}"; then sudo rm -rf -- "$STAGE_DIR"; fi; }
trap cleanup_stage EXIT INT TERM
sudo chmod 0755 "$STAGE_DIR"
sudo install -o root -g root -m 0755 gift-panel-release-mirror-linux-amd64 "$STAGED_BINARY"
sudo install -o root -g root -m 0644 gift-panel-release-mirror.reviewed "$STAGE_DIR/gift-panel-release-mirror.reviewed"
test "$(wc -l < "$STAGE_DIR/gift-panel-release-mirror.reviewed")" -eq 2
test "$RELEASE_ID" = "$(sed -n '1p' "$STAGE_DIR/gift-panel-release-mirror.reviewed")"
test "$REVIEWED_SHA256" = "$(sed -n '2p' "$STAGE_DIR/gift-panel-release-mirror.reviewed")"
printf '%s  %s\n' "$REVIEWED_SHA256" "$STAGED_BINARY" | sha256sum -c -
test "$RELEASE_ID" = "$("$STAGED_BINARY" --build-commit)"
STAGED_METADATA=$(go version -m "$STAGED_BINARY")
printf '%s\n' "$STAGED_METADATA" | grep -Fq 'github.com/brainfk123/bilibili-live-gift-panel/updateapi/cmd/mirror'
printf '%s\n' "$STAGED_METADATA" | grep -Fq 'GOOS=linux'
printf '%s\n' "$STAGED_METADATA" | grep -Fq 'GOARCH=amd64'
if sudo test -e "$FINAL_RELEASE"; then
  test ! -L "$FINAL_RELEASE"
  test "$(sudo find "$FINAL_RELEASE" -mindepth 1 -maxdepth 1 -printf '%f\n' | sort)" = "$(printf '%s\n' gift-panel-release-mirror gift-panel-release-mirror.reviewed | sort)"
  sudo cmp -s -- "$STAGED_BINARY" "$FINAL_BINARY"
  sudo cmp -s -- "$STAGE_DIR/gift-panel-release-mirror.reviewed" "$FINAL_RELEASE/gift-panel-release-mirror.reviewed"
  sudo rm -rf -- "$STAGE_DIR"
else
  sudo mv -T -- "$STAGE_DIR" "$FINAL_RELEASE"
fi
STAGE_DIR=
trap - EXIT INT TERM
printf '%s  %s\n' "$REVIEWED_SHA256" "$FINAL_BINARY" | sha256sum -c -
test "$RELEASE_ID" = "$("$FINAL_BINARY" --build-commit)"
test "$(readlink -f -- "$FINAL_BINARY")" = "$FINAL_BINARY"
sudo install -o root -g root -m 0644 deploy/update-api/gift-panel-release-mirror.service /etc/systemd/system/gift-panel-release-mirror.service
sudo install -o root -g root -m 0644 deploy/update-api/gift-panel-release-mirror.timer /etc/systemd/system/gift-panel-release-mirror.timer
sudo install -d -o root -g root -m 0755 /etc/systemd/journald@gift-panel-release-mirror.conf.d
sudo install -o root -g root -m 0644 deploy/update-api/journald.conf /etc/systemd/journald@gift-panel-release-mirror.conf.d/retention.conf
sudo systemctl daemon-reload
sudo systemd-analyze verify /etc/systemd/system/gift-panel-release-mirror.service /etc/systemd/system/gift-panel-release-mirror.timer
mirror_verify_quiesced
```

STOP: obtain separate action-time operator confirmation before installing the secret environment file. This is a distinct secret-bearing production mutation and is not authorized by transfer, account, binary, or unit approval.

```sh
set -euo pipefail
mirror_systemctl_value() {
  property=$1 unit=$2; value=$(sudo systemctl show --property="$property" --value "$unit") || return 1
  case "$value" in ''|*[!a-z-]*) return 1 ;; esac; printf '%s\n' "$value"
}
mirror_verify_quiesced() {
  timer_load=$(mirror_systemctl_value LoadState gift-panel-release-mirror.timer) || return 1
  timer_active=$(mirror_systemctl_value ActiveState gift-panel-release-mirror.timer) || return 1
  timer_unit_file=$(sudo systemctl show --property=UnitFileState --value gift-panel-release-mirror.timer) || return 1
  service_load=$(mirror_systemctl_value LoadState gift-panel-release-mirror.service) || return 1
  service_active=$(mirror_systemctl_value ActiveState gift-panel-release-mirror.service) || return 1
  test "$timer_load:$timer_active:$timer_unit_file" = loaded:inactive:disabled
  test "$service_load:$service_active" = loaded:inactive
}
mirror_verify_quiesced
sudo install -o root -g root -m 0600 /secure/gift-panel-release-mirror.env /etc/gift-panel-release-mirror.env
mirror_verify_quiesced
```

Do not run the binary manually on a fresh host: systemd must create `StateDirectory` first. The dry-run below revalidates the final binary and fail-closed state, records the prior `InvocationID`, starts only after the exact drop-in is active, and requires a new nonempty invocation identity. After cleanup it revalidates quiescence again before atomically changing `current`.

```sh
set -euo pipefail
test "$(wc -l < gift-panel-release-mirror.reviewed)" -eq 2
RELEASE_ID=$(sed -n '1p' gift-panel-release-mirror.reviewed)
REVIEWED_SHA256=$(sed -n '2p' gift-panel-release-mirror.reviewed)
printf '%s\n' "$RELEASE_ID" | grep -Eq '^[0-9a-f]{40}$'
FINAL_RELEASE=/opt/gift-panel-release-mirror/releases/"$RELEASE_ID"
FINAL_BINARY="$FINAL_RELEASE/gift-panel-release-mirror"
printf '%s  %s\n' "$REVIEWED_SHA256" "$FINAL_BINARY" | sha256sum -c -
test "$RELEASE_ID" = "$("$FINAL_BINARY" --build-commit)"
mirror_systemctl_value() {
  property=$1 unit=$2; value=$(sudo systemctl show --property="$property" --value "$unit") || return 1
  case "$value" in ''|*[!a-z-]*) return 1 ;; esac; printf '%s\n' "$value"
}
mirror_verify_quiesced() {
  timer_load=$(mirror_systemctl_value LoadState gift-panel-release-mirror.timer) || return 1
  timer_active=$(mirror_systemctl_value ActiveState gift-panel-release-mirror.timer) || return 1
  timer_unit_file=$(sudo systemctl show --property=UnitFileState --value gift-panel-release-mirror.timer) || return 1
  service_load=$(mirror_systemctl_value LoadState gift-panel-release-mirror.service) || return 1
  service_active=$(mirror_systemctl_value ActiveState gift-panel-release-mirror.service) || return 1
  test "$timer_load:$timer_active:$timer_unit_file" = loaded:inactive:disabled
  test "$service_load:$service_active" = loaded:inactive
}
mirror_verify_quiesced
BEFORE_INVOCATION=$(sudo systemctl show --property=InvocationID --value gift-panel-release-mirror.service) || exit 1
DROPIN=/run/systemd/system/gift-panel-release-mirror.service.d/dry-run.conf
DROPIN_DIR=$(dirname "$DROPIN")
test "$DROPIN_DIR" = /run/systemd/system/gift-panel-release-mirror.service.d
cleanup_dry_run() {
  cleanup_failed=0
  if test -e "$DROPIN"; then sudo rm -- "$DROPIN" || cleanup_failed=1; fi
  sudo systemctl daemon-reload || cleanup_failed=1
  test ! -e "$DROPIN" || cleanup_failed=1
  active_dropins=$(sudo systemctl show --property=DropInPaths --value gift-panel-release-mirror.service)
  if test $? -ne 0; then cleanup_failed=1
  elif printf '%s\n' "$active_dropins" | grep -Fq -- "$DROPIN"; then cleanup_failed=1
  fi
  return "$cleanup_failed"
}
finish_dry_run() {
  original_rc=$1; trap - EXIT INT TERM; set +e; cleanup_dry_run; cleanup_rc=$?; set -e
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
printf '[Service]\nExecStart=\nExecStart=%s --dry-run\n' "$FINAL_BINARY" | sudo tee "$DROPIN" >/dev/null
sudo systemctl daemon-reload
mirror_verify_quiesced
sudo systemctl start gift-panel-release-mirror.service
DRY_RUN_INVOCATION=$(sudo systemctl show --property=InvocationID --value gift-panel-release-mirror.service) || exit 1
test -n "$DRY_RUN_INVOCATION"
test "$DRY_RUN_INVOCATION" != "$BEFORE_INVOCATION"
test "$(sudo systemctl show --property=Result --value gift-panel-release-mirror.service)" = success
sudo journalctl --namespace=gift-panel-release-mirror -u gift-panel-release-mirror.service --no-pager
set +e
cleanup_dry_run
cleanup_rc=$?
set -e
test "$cleanup_rc" -eq 0
trap - EXIT INT TERM
mirror_verify_quiesced
CURRENT_TMP=/opt/gift-panel-release-mirror/.current-"$RELEASE_ID"
test ! -e "$CURRENT_TMP"
test ! -L "$CURRENT_TMP"
sudo ln -s -- "$FINAL_RELEASE" "$CURRENT_TMP"
sudo mv -Tf -- "$CURRENT_TMP" /opt/gift-panel-release-mirror/current
test "$(readlink -f /opt/gift-panel-release-mirror/current/gift-panel-release-mirror)" = "$FINAL_BINARY"
test "$RELEASE_ID" = "$(/opt/gift-panel-release-mirror/current/gift-panel-release-mirror --build-commit)"
mirror_verify_quiesced
```

Stop here and obtain independent operator confirmation before the real oneshot. The dry-run result and exact `current` target are review evidence; neither authorizes a COS write.

After that confirmation only, run one real oneshot and stop again:

```sh
set -euo pipefail
mirror_systemctl_value() {
  property=$1 unit=$2; value=$(sudo systemctl show --property="$property" --value "$unit") || return 1
  case "$value" in ''|*[!a-z-]*) return 1 ;; esac; printf '%s\n' "$value"
}
mirror_verify_quiesced() {
  test "$(mirror_systemctl_value LoadState gift-panel-release-mirror.timer)" = loaded
  test "$(mirror_systemctl_value ActiveState gift-panel-release-mirror.timer)" = inactive
  test "$(sudo systemctl show --property=UnitFileState --value gift-panel-release-mirror.timer)" = disabled
  test "$(mirror_systemctl_value LoadState gift-panel-release-mirror.service)" = loaded
  test "$(mirror_systemctl_value ActiveState gift-panel-release-mirror.service)" = inactive
}
mirror_verify_quiesced
BEFORE_INVOCATION=$(sudo systemctl show --property=InvocationID --value gift-panel-release-mirror.service) || exit 1
sudo systemctl start gift-panel-release-mirror.service
REAL_INVOCATION=$(sudo systemctl show --property=InvocationID --value gift-panel-release-mirror.service) || exit 1
test -n "$REAL_INVOCATION"
test "$REAL_INVOCATION" != "$BEFORE_INVOCATION"
test "$(sudo systemctl show --property=Result --value gift-panel-release-mirror.service)" = success
sudo journalctl --namespace=gift-panel-release-mirror -u gift-panel-release-mirror.service --no-pager
# real oneshot invocation is complete; timer remains disabled pending acceptance
mirror_verify_quiesced
```

Independently verify that `releases/v0.4.4/gift-panel-windows-x64.exe`, `releases/v0.4.4/gift-panel-windows-x64.exe.sha256`, `releases/v0.4.4/gift-panel-changelog.json`, and `releases/v0.4.4/release.json` all exist, match the reviewed GitHub size/SHA-256 or reviewed content as applicable, and remain immutable; verify `channels/stable/latest.json` points to the same complete release. Then verify the domestic latest and changelog routes; the signed download URL is redacted, and the existing update API service and `127.0.0.1:12450` listener must be unchanged. Capture only tag, asset name, size, digest, publication time, changelog ordering, and redacted URL evidence—never its query string.

Stop here and obtain a separate operator confirmation before enabling the timer. Only after the COS, domestic API, changelog, redaction, and unchanged-listener evidence has been independently reviewed may scheduling change:

```sh
set -euo pipefail
sudo systemctl enable --now gift-panel-release-mirror.timer
systemctl list-timers gift-panel-release-mirror.timer --all --no-pager
```

### Rotate Lighthouse mirror credentials

After an independently approved policy-simulator check, create a replacement `lighthouse-cos-publisher` key without disabling the active key. Install the replacement values through the approved secret channel into `/etc/gift-panel-release-mirror.env`, retaining owner `root:root` and mode `0600`; never put them in a command, shell history, log, ticket, commit, or chat. Repeat the same separately confirmed dry-run, real-oneshot, COS/API verification, and timer gates. Only after those checks succeed may a separately confirmed action revoke the old key. Delete that old key only in a later, independently confirmed cleanup window.

### Retire the obsolete GitHub publisher

Do not clean up the former GitHub publisher during installation. Production acceptance first requires a successful systemd dry-run, one verified real mirror, matching public API metadata, an enabled five-minute timer, a later no-op, and a failed-then-recovered retry that leaves stable unchanged during failure.

Only after that production acceptance, request separate explicit confirmation before each external cleanup stage. With approval, remove the obsolete GitHub Environment COS secrets and variables from the protected `release` Environment. In a separately approved key window, disable the old `github-cos-uploader` key, verify that `lighthouse-cos-publisher` still mirrors or no-ops successfully, and leave the old key disabled. Delete the old key only in a later independently confirmed cleanup window. Never combine Environment cleanup, key disablement, and key deletion into one approval.

For rollback, quiesce both timer and oneshot before inspecting or changing installed bits. The exact previous version must already contain its independently reviewed two-line sidecar. Preverify its safe path, complete directory contents, SHA-256, embedded 40-hex commit, and Linux amd64 Go command identity; the dry-run then points directly at that exact preverified version. If the stable pointer also needs to move backward, stop and use a separately confirmed Tencent COS console or approved operator action to restore the reviewed private `channels/stable/latest.json` backup while quiesced; do not re-enable timer while an intentionally older stable release would immediately be re-promoted. Do not delete immutable release objects.

```sh
set -euo pipefail
mirror_systemctl_value() {
  property=$1 unit=$2; value=$(sudo systemctl show --property="$property" --value "$unit") || return 1
  case "$value" in ''|*[!a-z-]*) return 1 ;; esac; printf '%s\n' "$value"
}
mirror_timer_unit_file_state() {
  timer_load=$1; raw=$(sudo systemctl show --property=UnitFileState --value gift-panel-release-mirror.timer) || return 1
  case "$timer_load:$raw" in loaded:disabled) printf '%s\n' disabled ;; not-found:|not-found:not-found) printf '%s\n' not-found ;; *) return 1 ;; esac
}
mirror_verify_quiesced() {
  timer_load=$(mirror_systemctl_value LoadState gift-panel-release-mirror.timer) || return 1
  timer_active=$(mirror_systemctl_value ActiveState gift-panel-release-mirror.timer) || return 1
  timer_unit_file=$(mirror_timer_unit_file_state "$timer_load") || return 1
  service_load=$(mirror_systemctl_value LoadState gift-panel-release-mirror.service) || return 1
  service_active=$(mirror_systemctl_value ActiveState gift-panel-release-mirror.service) || return 1
  case "$timer_load:$timer_active:$timer_unit_file" in loaded:inactive:disabled|not-found:inactive:not-found) ;; *) return 1 ;; esac
  case "$service_load:$service_active" in loaded:inactive|not-found:inactive) ;; *) return 1 ;; esac
}
mirror_quiesce() {
  timer_load=$(mirror_systemctl_value LoadState gift-panel-release-mirror.timer) || return 1
  service_load=$(mirror_systemctl_value LoadState gift-panel-release-mirror.service) || return 1
  case "$timer_load" in loaded) sudo systemctl disable --now gift-panel-release-mirror.timer || return 1 ;; not-found) ;; *) return 1 ;; esac
  case "$service_load" in loaded) sudo systemctl stop gift-panel-release-mirror.service || return 1 ;; not-found) ;; *) return 1 ;; esac
  mirror_verify_quiesced
}
mirror_quiesce
PREVIOUS_RELEASE_ID="${PREVIOUS_RELEASE_ID:?set the reviewed previous 40-hex commit}"
printf '%s\n' "$PREVIOUS_RELEASE_ID" | grep -Eq '^[0-9a-f]{40}$'
PREVIOUS_RELEASE=/opt/gift-panel-release-mirror/releases/"$PREVIOUS_RELEASE_ID"
PREVIOUS_BINARY="$PREVIOUS_RELEASE/gift-panel-release-mirror"
PREVIOUS_SIDECAR="$PREVIOUS_RELEASE/gift-panel-release-mirror.reviewed"
test ! -L "$PREVIOUS_RELEASE"
test ! -L "$PREVIOUS_BINARY"
test ! -L "$PREVIOUS_SIDECAR"
test "$(readlink -f -- "$PREVIOUS_RELEASE")" = "$PREVIOUS_RELEASE"
test "$(readlink -f -- "$PREVIOUS_BINARY")" = "$PREVIOUS_BINARY"
test "$(readlink -f -- "$PREVIOUS_SIDECAR")" = "$PREVIOUS_SIDECAR"
test "$(sudo find "$PREVIOUS_RELEASE" -mindepth 1 -maxdepth 1 -printf '%f\n' | sort)" = "$(printf '%s\n' gift-panel-release-mirror gift-panel-release-mirror.reviewed | sort)"
test "$(wc -l < "$PREVIOUS_SIDECAR")" -eq 2
test "$PREVIOUS_RELEASE_ID" = "$(sed -n '1p' "$PREVIOUS_SIDECAR")"
PREVIOUS_SHA256=$(sed -n '2p' "$PREVIOUS_SIDECAR")
printf '%s\n' "$PREVIOUS_SHA256" | grep -Eq '^[0-9a-f]{64}$'
printf '%s  %s\n' "$PREVIOUS_SHA256" "$PREVIOUS_BINARY" | sha256sum -c -
test "$PREVIOUS_RELEASE_ID" = "$("$PREVIOUS_BINARY" --build-commit)"
PREVIOUS_METADATA=$(go version -m "$PREVIOUS_BINARY")
printf '%s\n' "$PREVIOUS_METADATA" | grep -Fq 'github.com/brainfk123/bilibili-live-gift-panel/updateapi/cmd/mirror'
printf '%s\n' "$PREVIOUS_METADATA" | grep -Fq 'GOOS=linux'
printf '%s\n' "$PREVIOUS_METADATA" | grep -Fq 'GOARCH=amd64'
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
mirror_verify_quiesced
BEFORE_INVOCATION=$(sudo systemctl show --property=InvocationID --value gift-panel-release-mirror.service) || exit 1
DROPIN=/run/systemd/system/gift-panel-release-mirror.service.d/dry-run.conf
DROPIN_DIR=$(dirname "$DROPIN")
test "$DROPIN_DIR" = /run/systemd/system/gift-panel-release-mirror.service.d
cleanup_dry_run() {
  cleanup_failed=0
  if test -e "$DROPIN"; then sudo rm -- "$DROPIN" || cleanup_failed=1; fi
  sudo systemctl daemon-reload || cleanup_failed=1
  test ! -e "$DROPIN" || cleanup_failed=1
  active_dropins=$(sudo systemctl show --property=DropInPaths --value gift-panel-release-mirror.service)
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
printf '[Service]\nExecStart=\nExecStart=%s --dry-run\n' "$PREVIOUS_BINARY" | sudo tee "$DROPIN" >/dev/null
sudo systemctl daemon-reload
mirror_verify_quiesced
sudo systemctl start gift-panel-release-mirror.service
ROLLBACK_DRY_RUN_INVOCATION=$(sudo systemctl show --property=InvocationID --value gift-panel-release-mirror.service) || exit 1
test -n "$ROLLBACK_DRY_RUN_INVOCATION"
test "$ROLLBACK_DRY_RUN_INVOCATION" != "$BEFORE_INVOCATION"
test "$(sudo systemctl show --property=Result --value gift-panel-release-mirror.service)" = success
sudo journalctl --namespace=gift-panel-release-mirror -u gift-panel-release-mirror.service --no-pager
set +e
cleanup_dry_run
cleanup_rc=$?
set -e
test "$cleanup_rc" -eq 0
trap - EXIT INT TERM
# Only after this rollback dry-run succeeds is the local pointer changed.
mirror_verify_quiesced
CURRENT_TMP=/opt/gift-panel-release-mirror/.current-rollback-"$PREVIOUS_RELEASE_ID"
test ! -e "$CURRENT_TMP"
test ! -L "$CURRENT_TMP"
sudo ln -s -- "$PREVIOUS_RELEASE" "$CURRENT_TMP"
sudo mv -Tf -- "$CURRENT_TMP" /opt/gift-panel-release-mirror/current
test "$(readlink -f /opt/gift-panel-release-mirror/current/gift-panel-release-mirror)" = "$PREVIOUS_BINARY"
test "$PREVIOUS_RELEASE_ID" = "$(/opt/gift-panel-release-mirror/current/gift-panel-release-mirror --build-commit)"
mirror_verify_quiesced
```

The rollback remains quiesced after the atomic switch. Verify the intended domestic latest/changelog state and unchanged API listener, then obtain independent confirmation for any later real mirror run or timer enablement; do not combine those actions.
