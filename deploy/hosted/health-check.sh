#!/usr/bin/env bash
set -euo pipefail
umask 077

# Stable codes for Tencent monitoring:
# 0 ok, 2 configuration, 10 loopback health, 11 disk, 12 Compose,
# 13 backup age, 14 certificate expiry, 15 log archive age.

readonly curl_bin="${HOSTED_CURL_BIN:-curl}"
readonly df_bin="${HOSTED_DF_BIN:-df}"
readonly docker_bin="${HOSTED_DOCKER_BIN:-docker}"
readonly openssl_bin="${HOSTED_OPENSSL_BIN:-openssl}"
readonly date_bin="${HOSTED_DATE_BIN:-date}"
readonly compose_file="${HOSTED_COMPOSE_FILE:-/opt/gift-panel-hosted/current/deploy/hosted/docker-compose.yml}"
readonly health_url="${HOSTED_HEALTH_URL:-http://127.0.0.1:12500/healthz}"
readonly disk_path="${HOSTED_DISK_PATH:-/}"
readonly disk_max_percent="${HOSTED_DISK_MAX_PERCENT:-85}"
readonly backup_state_root="${HOSTED_BACKUP_STATE_ROOT:-/var/lib/gift-panel-hosted-backup}"
readonly tls_certificate="${HOSTED_TLS_CERTIFICATE:?set HOSTED_TLS_CERTIFICATE}"
readonly archive_state_root="${HOSTED_ARCHIVE_STATE_ROOT:-/var/lib/gift-panel-hosted-log-archive}"
readonly cert_min_days="${HOSTED_CERT_MIN_DAYS:-21}"

[[ "$health_url" == 'http://127.0.0.1:12500/healthz' ]] || exit 2
[[ "$disk_max_percent" =~ ^[1-9][0-9]?$ ]] || exit 2
[[ "$cert_min_days" =~ ^[1-9][0-9]*$ ]] || exit 2
[[ -r "$compose_file" && ! -L "$compose_file" ]] || exit 2
[[ -r "$tls_certificate" && ! -L "$tls_certificate" ]] || exit 2
[[ -d "$backup_state_root" && ! -L "$backup_state_root" && -r "$backup_state_root/daily.next" && ! -L "$backup_state_root/daily.next" ]] || exit 2
[[ -d "$archive_state_root" && ! -L "$archive_state_root" && -r "$archive_state_root/next-day" && ! -L "$archive_state_root/next-day" ]] || exit 2

if ! "$curl_bin" --fail --silent --show-error --max-time 5 "$health_url" >/dev/null; then
  printf '%s\n' 'loopback health check failed' >&2
  exit 10
fi

df_output="$("$df_bin" -P "$disk_path")"
disk_percent="$(printf '%s\n' "$df_output" | awk 'NR==2 { gsub(/%/, "", $5); print $5 }')"
[[ "$disk_percent" =~ ^[0-9]+$ ]] || exit 2
if (( disk_percent >= disk_max_percent )); then
  printf '%s\n' 'disk usage exceeds threshold' >&2
  exit 11
fi

if ! compose_health="$("$docker_bin" compose -f "$compose_file" ps --format '{{.Service}} {{.Health}}')"; then
  printf '%s\n' 'compose health query failed' >&2
  exit 12
fi
printf '%s\n' "$compose_health" | grep -qx 'mysql healthy' || {
  printf '%s\n' 'mysql compose health failed' >&2
  exit 12
}
printf '%s\n' "$compose_health" | grep -qx 'app healthy' || {
  printf '%s\n' 'app compose health failed' >&2
  exit 12
}

IFS= read -r backup_next <"$backup_state_root/daily.next"
[[ "$backup_next" =~ ^[0-9]{4}-[0-9]{2}-[0-9]{2}$ ]] || exit 2
today="$("$date_bin" -u +%F)"
[[ "$today" =~ ^[0-9]{4}-[0-9]{2}-[0-9]{2}$ ]] || exit 2
if [[ "$backup_next" < "$today" ]]; then
  printf '%s\n' 'backup age exceeds one calendar day' >&2
  exit 13
fi

not_after="$("$openssl_bin" x509 -noout -enddate -in "$tls_certificate")"
[[ "$not_after" == notAfter=* ]] || exit 2
expiry_epoch="$("$date_bin" -u -d "${not_after#notAfter=}" +%s)"
now_epoch="$("$date_bin" -u +%s)"
[[ "$expiry_epoch" =~ ^[0-9]+$ && "$now_epoch" =~ ^[0-9]+$ ]] || exit 2
min_seconds=$((cert_min_days * 86400))
if (( expiry_epoch < now_epoch + min_seconds )); then
  printf '%s\n' 'certificate expiry is below threshold' >&2
  exit 14
fi

IFS= read -r archive_next <"$archive_state_root/next-day"
[[ "$archive_next" =~ ^[0-9]{4}-[0-9]{2}-[0-9]{2}$ ]] || exit 2
archive_earliest="$("$date_bin" -u -d "$today - 34 days" +%F)"
[[ "$archive_earliest" =~ ^[0-9]{4}-[0-9]{2}-[0-9]{2}$ ]] || exit 2
if [[ "$archive_next" < "$archive_earliest" ]]; then
  printf '%s\n' 'log archive age exceeds retention window' >&2
  exit 15
fi
