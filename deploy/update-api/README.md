# Gift Panel Update API deployment

This deployment serves private COS release metadata through the API only. Keep the COS bucket private; do not enable public read or configure a CDN. Task 4 gate: COS Versioning must not be Enabled. Safest is never-enabled versioning. If versioning is Suspended, run controlled staging verification before production, including immutable release writes and stable-manifest promotion. The API issues short-lived signed download URLs, and neither Nginx access logs nor this runbook records their query strings.

## Required names

GitHub Actions variables: `UPDATE_API_BASE_URL`, `COS_BUCKET`, `COS_REGION`, `EVSIGN_EXPECTED_SUBJECT`.

GitHub Actions secrets: `COS_RELEASE_SECRET_ID`, `COS_RELEASE_SECRET_KEY`.

Server environment variables: `UPDATE_API_LISTEN`, `COS_BUCKET`, `COS_REGION`, `COS_SECRET_ID`, `COS_SECRET_KEY`, `COS_CHANNEL_KEY`.

Rendering variables: `PUBLIC_DOMAIN`, `ICP_NUMBER`, `TLS_CERT_PATH`, `TLS_KEY_PATH`.

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

## Verify and operate

Run health checks locally from the server and only call public API routes over HTTPS:

```sh
curl --fail --silent --show-error http://127.0.0.1:12450/healthz | grep -Fx 'ok'
curl --fail --silent --show-error https://PUBLIC_DOMAIN/api/v1/releases/latest
curl --fail --silent --show-error https://PUBLIC_DOMAIN/api/v1/changelog
```

Before changing the stable channel, back up the current private COS object `channels/stable/latest.json` to a dated private key such as `channels/stable/backups/DATE/latest.json` using the approved COS operator tooling. To roll back, restore that verified backup to `channels/stable/latest.json`; do not overwrite immutable `releases/` objects. Restart the service only if its credentials or binary changed.

Rotate COS credentials by creating a replacement least-privilege key, updating `/etc/gift-panel-update-api.env` with root ownership and mode `0600`, running `sudo systemctl restart gift-panel-update-api.service`, verifying the local health and HTTPS API checks above, then revoking the old key. Do not copy credentials, signed URLs, or their query strings into tickets, shell history, logs, commits, or chat.

For an Nginx rollback, restore the last known-good `/etc/nginx/conf.d/gift-panel-update-api.conf`, run `sudo nginx -t`, and then `sudo systemctl reload nginx.service`. To roll back the API binary, repoint `/opt/gift-panel-update-api/current` to the previous release directory and restart `gift-panel-update-api.service` after the same verification checks.
