#!/usr/bin/env bash
set -euo pipefail
umask 077

readonly docker_bin="${HOSTED_DOCKER_BIN:-docker}"
readonly cos_bin="${HOSTED_COS_BIN:-coscli}"
readonly zstd_bin="${HOSTED_ZSTD_BIN:-zstd}"
readonly age_bin="${HOSTED_AGE_BIN:-age}"
readonly timeout_bin="${HOSTED_TIMEOUT_BIN:-timeout}"
readonly external_timeout="${HOSTED_EXTERNAL_TIMEOUT:-10m}"
readonly cos_config="${HOSTED_COS_CONFIG_FILE:?set HOSTED_COS_CONFIG_FILE}"
readonly identity_file="${HOSTED_AGE_IDENTITY_FILE:?set HOSTED_AGE_IDENTITY_FILE}"
readonly selected_object="${HOSTED_RESTORE_OBJECT:?set HOSTED_RESTORE_OBJECT}"
readonly report_root="${HOSTED_RESTORE_REPORT_ROOT:?set HOSTED_RESTORE_REPORT_ROOT}"
readonly temporary_root="${TMPDIR:-/tmp}"

[[ "${HOSTED_COS_REGION:?set HOSTED_COS_REGION}" == "ap-hongkong" ]] || exit 2
[[ "$external_timeout" =~ ^[1-9][0-9]*(s|m|h)$ ]] || exit 2
[[ -r "$cos_config" && -r "$identity_file" ]] || exit 2
[[ "$selected_object" =~ ^cos://[a-z0-9][a-z0-9-]*-[0-9]+/hosted/backups/(daily|weekly|monthly)/gift-panel-([0-9]{8})-([0-9]{8}T[0-9]{6}Z)-([0-9a-f]{16})\.sql\.zst\.age$ ]] || exit 2
readonly backup_period="${BASH_REMATCH[1]}"
readonly scheduled_compact="${BASH_REMATCH[2]}"
readonly rpo_timestamp="${BASH_REMATCH[3]}"
readonly scheduled_day="${scheduled_compact:0:4}-${scheduled_compact:4:2}-${scheduled_compact:6:2}"
[[ -d "$report_root" ]] || exit 2

started_epoch="$(date -u +%s)"
timestamp="$(date -u +%Y%m%dT%H%M%SZ)"
random_suffix="$(od -An -N6 -tx1 /dev/urandom | tr -d ' \n')"
project_name="gift-panel-restore-${timestamp,,}-${random_suffix}"
volume_name="${project_name}-mysql-data"
work_dir="$(mktemp -d "$temporary_root/gift-panel-restore-work.XXXXXXXX")"
chmod 0700 -- "$work_dir"
compose_file="$work_dir/docker-compose.yml"
container_started=false
resolved_volume=''

run_external() {
  "$timeout_bin" --foreground "$external_timeout" "$@"
}

cleanup_container() {
  if [[ "$project_name" != gift-panel-restore-* ]]; then
    printf '%s\n' 'refusing cleanup for unexpected project' >&2
    return 1
  fi
  resolved_volume="$(run_external "$docker_bin" volume ls --filter "label=com.docker.compose.project=$project_name" --format '{{.Name}}')" || return 1
  if [[ -z "$resolved_volume" || "$resolved_volume" == *$'\n'* || "$resolved_volume" != "$volume_name" ]]; then
    printf '%s\n' 'refusing cleanup for unexpected resolved volume' >&2
    return 1
  fi
  run_external "$docker_bin" compose -p "$project_name" -f "$compose_file" down --volumes --remove-orphans || return 1
  container_started=false
}

cleanup() {
  local original_status=$?
  local cleanup_status=0
  local preserve_work_dir=false
  trap - EXIT
  if [[ "$container_started" == true ]]; then
    if ! cleanup_container; then
      cleanup_status=1
      preserve_work_dir=true
      printf 'restore cleanup failed; retained remediation directory: %s\n' "$work_dir" >&2
      printf 'risk: disposable restore project may still be running; inspect only exact project %s\n' "$project_name" >&2
    fi
  fi
  if [[ "$preserve_work_dir" == false && -n "${work_dir:-}" && -d "$work_dir" && "$work_dir" == "$temporary_root/gift-panel-restore-work."* ]]; then
    find "$work_dir" -depth -mindepth 1 -delete || cleanup_status=1
    rmdir -- "$work_dir" || cleanup_status=1
  fi
  if [[ "$original_status" -ne 0 ]]; then
    exit "$original_status"
  fi
  exit "$cleanup_status"
}
trap 'cleanup' EXIT

cat >"$compose_file" <<EOF
services:
  mysql:
    image: mysql:8.4.11@sha256:1d6b6a8fcee8ff758ff151d017f5203cd06792a0e698f0a593c9dfcb14609cf0
    environment:
      MYSQL_ROOT_PASSWORD: ${random_suffix}
      MYSQL_DATABASE: gift_panel
    volumes:
      - restore_mysql_data:/var/lib/mysql
    healthcheck:
      test: ["CMD-SHELL", "mysqladmin ping --host=127.0.0.1 --silent"]
      interval: 2s
      timeout: 2s
      retries: 60
volumes:
  restore_mysql_data:
    name: ${volume_name}
EOF

artifact_name="${selected_object##*/}"
artifact_file="$work_dir/$artifact_name"
checksum_file="$artifact_file.sha256"
completion_file="$artifact_file.complete"
compressed_file="$work_dir/${artifact_name%.age}"
sql_file="$work_dir/${artifact_name%.zst.age}"

run_external "$cos_bin" cp "$selected_object.complete" "$completion_file" --config-path "$cos_config" --endpoint cos.ap-hongkong.myqcloud.com --disable-log --process-log=false --forbid-overwrite
completion_contents="$(cat -- "$completion_file")"
expected_completion="$(printf 'artifact=%s\nperiod=%s\nscheduled_utc=%s\ndelivery=at-least-once\ncompleted_utc=%s\n' \
  "$artifact_name" "$backup_period" "$scheduled_day" "$rpo_timestamp")"
[[ "$completion_contents" == "$expected_completion" ]] || exit 4
run_external "$cos_bin" cp "$selected_object" "$artifact_file" --config-path "$cos_config" --endpoint cos.ap-hongkong.myqcloud.com --disable-log --process-log=false --forbid-overwrite
run_external "$cos_bin" cp "$selected_object.sha256" "$checksum_file" --config-path "$cos_config" --endpoint cos.ap-hongkong.myqcloud.com --disable-log --process-log=false --forbid-overwrite
mapfile -t checksum_lines <"$checksum_file"
readonly checksum_pattern='^([0-9a-f]{64})  ([A-Za-z0-9._-]+)$'
[[ "${#checksum_lines[@]}" -eq 1 ]] || exit 4
[[ "${checksum_lines[0]}" =~ $checksum_pattern ]] || exit 4
[[ "${BASH_REMATCH[2]}" == "$artifact_name" ]] || exit 4
(
  cd "$work_dir"
  sha256sum --check "${artifact_name}.sha256"
)
run_external "$age_bin" --decrypt --identity "$identity_file" --output "$compressed_file" "$artifact_file"
run_external "$zstd_bin" --decompress --quiet -o "$sql_file" "$compressed_file"

container_started=true
run_external "$docker_bin" compose -p "$project_name" -f "$compose_file" up -d --wait --wait-timeout 120
run_external "$docker_bin" compose -p "$project_name" -f "$compose_file" exec -T mysql sh -euc '
  MYSQL_PWD="$MYSQL_ROOT_PASSWORD"
  export MYSQL_PWD
  exec mysql --user=root gift_panel
' <"$sql_file"

mysql_query() {
  run_external "$docker_bin" compose -p "$project_name" -f "$compose_file" exec -T mysql sh -euc '
    MYSQL_PWD="$MYSQL_ROOT_PASSWORD"
    export MYSQL_PWD
    exec mysql --user=root --batch --skip-column-names gift_panel --execute "$1"
  ' sh "$1"
}

schema_count="$(mysql_query 'SELECT COUNT(*) FROM schema_migrations;')"
table_count="$(mysql_query "SELECT COUNT(*) FROM information_schema.tables WHERE table_schema = 'gift_panel';")"
invariant_violations="$(mysql_query 'SELECT (SELECT COUNT(*) FROM streamer_accounts WHERE credential_epoch < 1) + (SELECT COUNT(*) FROM invitation_quotas WHERE remaining_quota < 0);')"
[[ "$schema_count" =~ ^[1-9][0-9]*$ && "$table_count" =~ ^[1-9][0-9]*$ && "$invariant_violations" == 0 ]] || exit 5

if ! cleanup_container; then
  exit 6
fi
finished_epoch="$(date -u +%s)"
rto_seconds="$((finished_epoch - started_epoch))"
report_file="$report_root/${project_name}.txt"
printf 'backup_object=%s\nRPO_timestamp_utc=%s\nRTO_seconds=%s\nschema_migrations=%s\ntable_count=%s\ninvariant_violations=%s\n' \
  "$selected_object" "$rpo_timestamp" "$rto_seconds" "$schema_count" "$table_count" "$invariant_violations" >"$report_file"
printf 'restore_drill_report=%s\n' "$report_file"
