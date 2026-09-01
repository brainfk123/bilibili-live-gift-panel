# Gift Panel Update API deployment

This deployment serves private COS release metadata through the API only. Keep the COS bucket private; do not enable public read or configure a CDN. Task 4 production gate: COS Versioning state must be `Disabled`; `Suspended` is not acceptable. A bucket that was previously enabled or suspended cannot satisfy this gate because Task 4's overwrite-forbidden immutable writes are ineffective there. Provision a new never-versioned production bucket, or adopt and verify a redesigned immutable mechanism before using that bucket for releases. The API issues short-lived signed download URLs, and neither Nginx access logs nor this runbook records their query strings.

## Required names

Stable GitHub Actions variables are `UPDATE_API_BASE_URL`, `EVSIGN_CERTIFICATE`, `EVSIGN_PUBLISHER_IDENTITY`, and `RELEASE_TOOLING_COMMIT_SHA`.

`EVSIGN_CERTIFICATE` is the reviewed provider certificate selector. `EVSIGN_PUBLISHER_IDENTITY` is exactly `{"country":"CN","organization":"NaisNet Technology Co., Ltd.","organizationId":"91210103MA7CJ3C094"}`. The closed stable profile rejects missing, unknown, bridge, legacy, or free-form Subject configuration before signing. Verification parses Authenticode certificate DER and requires one C, one O, one Subject serialNumber (`2.5.4.5`), and Code Signing EKU; display Subject, thumbprint, and RDN ordering are not trust inputs.

The dedicated bridge workflow has distinct protected certificate/identity/credential names and cannot be selected by the stable workflow. See `docs/runbooks/bridge-release.md` for its approval-only public inputs; do not duplicate bridge secrets in deployment files.

GitHub Actions secrets: `EVSIGN_KEY`, `EVSIGN_PASSWORD`.

Store these signing variables and secrets only in the protected GitHub Environment `release`, with its approval and branch rules enabled. The workflow prebuilds security tooling from `RELEASE_TOOLING_COMMIT_SHA`, then treats the requested tag only as target source/assets. Exact `v0.4.11` is rejected before the protected environment because only the bridge workflow owns it. A validated GitHub Release is the workflow's terminal success condition; the workflow does not hold COS credentials or invoke the COS publisher.

Server environment variables are `UPDATE_API_LISTEN`, `COS_BUCKET`, `COS_REGION`, `COS_SECRET_ID`, `COS_SECRET_KEY`, `UPDATE_STABLE_CHANNEL_KEY`, `UPDATE_LEGACY_CHANNEL_KEY`, `UPDATE_LEGACY_ROUTING_ACTIVE`, and `UPDATE_PUBLISHER_POLICY_KEY`. The last four form a closed typed configuration: the only accepted object keys are `channels/stable/latest.json`, `channels/legacy-rushrush/latest.json`, and `trust/publisher/latest.json`; activation accepts only exact `true` or `false`. Omitted routing variables use those reviewed keys and keep legacy inactive. The production file must nevertheless spell out all four, with `UPDATE_LEGACY_ROUTING_ACTIVE=false`, so the candidate diff is reviewable.

Mirror environment variables are only `COS_BUCKET`, `COS_REGION`, `COS_SECRET_ID`, and `COS_SECRET_KEY`. Stable uses system account `gift-panel-mirror`, CAM identity `lighthouse-cos-publisher`, and root-owned `/etc/gift-panel-release-mirror.env`; legacy uses distinct system account `gift-panel-legacy-mirror`, CAM identity `lighthouse-cos-legacy-publisher`, and root-owned `/etc/gift-panel-legacy-release-mirror.env`. Each file is mode `0600`, contains different credentials, and must not be copied into GitHub. Neither the API nor either mirror receives a KMS provider variable or KMS Sign permission.

Rendering variables: `PUBLIC_DOMAIN`, `ICP_NUMBER`, `TLS_CERT_PATH`, `TLS_KEY_PATH`.

The existing `gift-panel-release-mirror.timer` checks the public GitHub Release asynchronously every five minutes and still invokes `gift-panel-release-mirror.service`, now explicitly as `mirror --channel stable`. GitHub publication never waits for the mirror. The oneshot validates all required public assets before constructing a COS client, preserves immutable release objects, and advances the stable pointer only after complete verification. `gift-panel-legacy-release-mirror.service` is a dormant oneshot template for exact `mirror --channel legacy-rushrush --tag v0.4.11`; there is no legacy timer and it must not be enabled or started during this staging task.

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

### Stage version-aware routing with legacy inactive

The routing environment owns exactly three namespaces: `UPDATE_STABLE_`, `UPDATE_LEGACY_`, and `UPDATE_PUBLISHER_`. Startup enumerates all process entries in those namespaces before applying defaults and rejects unknown, malformed, duplicate, empty, or non-reviewed values with one generic error. Normal unrelated variables, including `UPDATE_API_LISTEN`, are outside that boundary.

First run the credential-free local harness from the repository root:

```sh
go -C updateapi run ./cmd/routecheck
```

It uses an in-memory fixture Store and the real service, router, and HTTP handler composition. It performs no COS or network read/write and has no server fake-store switch. It covers every reviewed stable User-Agent, inactive v0.4.7, active legacy missing/malformed/wrong-channel pointers without stable fallback, invalid User-Agent forms, and the policy endpoint. The embedded public test policy is verified locally against its test SPKI before the endpoint body is accepted. Output is limited to canonical case, HTTP status, channel, bounded outcome, and the terminal `routecheck=ok cases=13`; a passing test fixture is evidence about routing composition, not production policy or pointer state.

The following candidate check is a local/read-only deployment gate, not deployment authorization. Start the candidate API on an unused loopback port with a copy of the proposed root-owned environment and `UPDATE_LEGACY_ROUTING_ACTIVE=false`. Use the captured public User-Agent values verbatim and record only status, bounded error code, and `X-Gift-Panel-Update-Channel`; never record signed download query strings.

```sh
set -euo pipefail
CANDIDATE_URL=http://127.0.0.1:12451
check_route() {
  expected_status=$1 expected_channel=$2 user_agent=$3
  headers=$(mktemp) body=$(mktemp)
  trap 'rm -f -- "$headers" "$body"' RETURN
  status=$(curl --silent --show-error --output "$body" --dump-header "$headers" --write-out '%{http_code}' -H "User-Agent: $user_agent" "$CANDIDATE_URL/api/v1/releases/latest")
  test "$status" = "$expected_status"
  channel=$(tr -d '\r' < "$headers" | awk -F ': ' 'tolower($1)=="x-gift-panel-update-channel" {print $2}')
  test "$channel" = "$expected_channel"
  rm -f -- "$headers" "$body"
  trap - RETURN
}
check_route 503 '' 'bilibili-live-gift-panel/0.4.7'
check_route 200 stable 'bilibili-live-gift-panel/0.4.9'
check_route 200 stable 'bilibili-live-gift-panel/0.4.10'
```

The v0.4.7 result must remain controlled unavailable even if `channels/legacy-rushrush/latest.json` already exists. It must never fall back to stable. Repeat v0.4.7 with an explicitly active local configuration against missing, malformed, and wrong-channel legacy fixtures; each must return controlled unavailable and must never read or return stable.

Exercise fail-closed request handling separately: missing User-Agent, leading/trailing whitespace, duplicate User-Agent headers, prerelease/development versions, oversized values, and unknown versions must return HTTP 400 with `client_version_invalid`. The stable v0.4.9 and v0.4.10 checks must retain `Vary: User-Agent` and `Cache-Control: private, no-store`.

Before and after the dry-run, read `channels/stable/latest.json` with the approved read-only COS tooling into separate mode-`0600` temporary files. Require byte equality with `cmp --silent` and matching `sha256sum`; do not run either mirror oneshot. The stable pointer and its hash must be unchanged while legacy is inactive.

Verify the policy endpoint locally rather than trusting its shape. First run the reviewed `trustpolicy verify-bundle` command against the committed policy/audit bundle and reviewed P-256 SPKI. Save its verification envelope, decode `.policy.bytesBase64`, and require byte equality with `curl --fail --silent --show-error "$CANDIDATE_URL/api/v1/trust/publisher-policy"`. A JSON parse or field check alone is not signature verification.

Record an exact byte comparison and SHA-256 for the installed and candidate binaries. For configuration, compare the complete root-owned installed and candidate files in the approved secure session, but export only a redacted diff that preserves the four routing names/values and reports credential fields as changed/unchanged without values. Confirm the candidate still has `UPDATE_LEGACY_ROUTING_ACTIVE=false`. Show the exact install, restart, health-check, and rollback commands together with these artifacts, then stop and obtain a separate action-time deployment confirmation before copying files, changing `current`, running `daemon-reload`, or restarting any service.

Rollback restores the reviewed prior API binary and complete prior configuration, explicitly sets `UPDATE_LEGACY_ROUTING_ACTIVE=false`, restarts only the API after separate confirmation, and repeats the route matrix and local policy verification. A bridge rollback may restore or remove only the reviewed legacy pointer through a separately confirmed operator action; it never starts the stable mirror and never mutates `channels/stable/latest.json`.

### Mirror permission and scheduler boundaries

Review the two CAM identities independently in the provider policy simulator before creating or changing credentials. The stable identity may Head/Get/Put only the reviewed immutable stable release objects and `channels/stable/latest.json`; it must be denied `channels/legacy-rushrush/latest.json`, the trust-policy prefix, Delete/List/bucket administration, and every KMS action. The legacy identity may Head/Get/Put only the exact reviewed `releases/v0.4.11/` objects and `channels/legacy-rushrush/latest.json`; it must be denied `channels/stable/latest.json`, other release tags, the trust-policy prefix, Delete/List/bucket administration, and every KMS action. The API identity remains read-only for its reviewed release, channel, and policy objects and has no KMS action.

Install the legacy environment and service file only after a separately approved bridge staging window. Do not create a legacy timer, add `WantedBy`, enable the oneshot, or alter `gift-panel-release-mirror.timer`. Separate state roots, system users, environment files, journals, locks, and ETags are mandatory; a dry-run or real invocation of one channel must not reuse the other's state.

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
printf '[Service]\nExecStart=\nExecStart=%s --channel stable --dry-run\n' "$FINAL_BINARY" | sudo tee "$DROPIN" >/dev/null
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
printf '[Service]\nExecStart=\nExecStart=%s --channel stable --dry-run\n' "$PREVIOUS_BINARY" | sudo tee "$DROPIN" >/dev/null
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
