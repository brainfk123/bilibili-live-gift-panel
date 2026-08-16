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

The GitHub Release mirror is deliberately separate from the update API: it has a different no-login account, a write-only COS CAM identity, `/var/lib/gift-panel-release-mirror` state managed by systemd, and a dedicated journal namespace. It is a oneshot process and opens no public socket. Build it only from the exact reviewed commit that will be installed. `npm run build:update-api` rejects tracked-dirty or unavailable Git identity; `GIFT_PANEL_LOCAL_BUILD=1` is solely for a non-deployment local build.

Create the dedicated service account and a versioned release location. Replace `RELEASE_ID` with the reviewed 40-hex commit and `REVIEWED_SHA256` with the independently reviewed checksum for this binary:

```sh
sudo useradd --system --user-group --home-dir /nonexistent --shell /usr/sbin/nologin gift-panel-mirror
npm run build:update-api
dist/gift-panel-release-mirror-linux-amd64 --build-commit
printf '%s  %s\n' 'REVIEWED_SHA256' 'dist/gift-panel-release-mirror-linux-amd64' | sha256sum -c -
sudo install -d -o root -g root -m 0755 /opt/gift-panel-release-mirror/releases/RELEASE_ID
sudo install -o root -g root -m 0755 dist/gift-panel-release-mirror-linux-amd64 /opt/gift-panel-release-mirror/releases/RELEASE_ID/gift-panel-release-mirror
sudo ln -sfn /opt/gift-panel-release-mirror/releases/RELEASE_ID /opt/gift-panel-release-mirror/current
```

Create the environment through an approved secret channel. It must contain only the mirror COS write credentials and must not reuse `/etc/gift-panel-update-api.env`:

```sh
sudo install -o root -g root -m 0600 /secure/gift-panel-release-mirror.env /etc/gift-panel-release-mirror.env
sudo install -o root -g root -m 0644 deploy/update-api/gift-panel-release-mirror.service /etc/systemd/system/gift-panel-release-mirror.service
sudo install -o root -g root -m 0644 deploy/update-api/gift-panel-release-mirror.timer /etc/systemd/system/gift-panel-release-mirror.timer
sudo install -d -o root -g root -m 0755 /etc/systemd/journald@gift-panel-release-mirror.conf.d
sudo install -o root -g root -m 0644 deploy/update-api/journald.conf /etc/systemd/journald@gift-panel-release-mirror.conf.d/retention.conf
sudo systemctl daemon-reload
sudo systemd-analyze verify /etc/systemd/system/gift-panel-release-mirror.service /etc/systemd/system/gift-panel-release-mirror.timer
```

Do not enable the timer until these validations succeed. The dry run fetches and validates the candidate without COS publication; the following one-shot invocation is the first controlled publication:

```sh
sudo -u gift-panel-mirror /opt/gift-panel-release-mirror/current/gift-panel-release-mirror --dry-run
sudo systemctl start gift-panel-release-mirror.service
sudo journalctl --namespace=gift-panel-release-mirror -u gift-panel-release-mirror.service --no-pager
sudo systemctl enable --now gift-panel-release-mirror.timer
```

To roll back the installed binary, stop the timer, repoint only the stable `current` pointer to a previously reviewed release directory, validate one controlled invocation, then enable the timer again. Do not delete immutable release objects in COS or release directories during rollback.

```sh
sudo systemctl disable --now gift-panel-release-mirror.timer
sudo ln -sfn /opt/gift-panel-release-mirror/releases/PREVIOUS_RELEASE_ID /opt/gift-panel-release-mirror/current
sudo systemctl start gift-panel-release-mirror.service
sudo systemctl enable --now gift-panel-release-mirror.timer
```
