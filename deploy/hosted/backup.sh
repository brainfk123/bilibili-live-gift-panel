#!/usr/bin/env bash
set -euo pipefail
umask 077

readonly docker_bin="${HOSTED_DOCKER_BIN:-docker}"
readonly cos_bin="${HOSTED_COS_BIN:-coscli}"
readonly zstd_bin="${HOSTED_ZSTD_BIN:-zstd}"
readonly age_bin="${HOSTED_AGE_BIN:-age}"
readonly timeout_bin="${HOSTED_TIMEOUT_BIN:-timeout}"
readonly external_timeout="${HOSTED_EXTERNAL_TIMEOUT:-10m}"
readonly date_bin="${HOSTED_DATE_BIN:-date}"
readonly compose_file="${HOSTED_COMPOSE_FILE:-/opt/gift-panel-hosted/current/deploy/hosted/docker-compose.yml}"
readonly cos_config="${HOSTED_COS_CONFIG_FILE:?set HOSTED_COS_CONFIG_FILE}"
readonly cos_bucket="${HOSTED_COS_BUCKET:?set HOSTED_COS_BUCKET}"
readonly recipient_file="${HOSTED_AGE_RECIPIENT_FILE:?set HOSTED_AGE_RECIPIENT_FILE}"
readonly backup_state_root="${HOSTED_BACKUP_STATE_ROOT:-/var/lib/gift-panel-hosted-backup}"
readonly lock_file="${HOSTED_BACKUP_LOCK_FILE:-/run/lock/gift-panel-hosted-backup.lock}"
readonly temporary_root="${TMPDIR:-/tmp}"

[[ "${HOSTED_COS_REGION:?set HOSTED_COS_REGION}" == "ap-hongkong" ]] || {
  printf '%s\n' 'HOSTED_COS_REGION must be ap-hongkong' >&2
  exit 2
}
[[ "$external_timeout" =~ ^[1-9][0-9]*(s|m|h)$ ]] || exit 2
[[ "$cos_bucket" =~ ^[a-z0-9][a-z0-9-]*-[0-9]+$ ]] || {
  printf '%s\n' 'HOSTED_COS_BUCKET is invalid' >&2
  exit 2
}
[[ -r "$cos_config" && -r "$recipient_file" && -r "$compose_file" ]] || {
  printf '%s\n' 'backup configuration is unreadable' >&2
  exit 2
}
[[ -d "$backup_state_root" && ! -L "$backup_state_root" && -w "$backup_state_root" ]] || {
  printf '%s\n' 'backup state directory is unavailable' >&2
  exit 2
}

mkdir -p -- "$(dirname -- "$lock_file")"
exec 9>"$lock_file"
flock -n 9 || {
  printf '%s\n' 'backup already running' >&2
  exit 3
}

work_dir="$(mktemp -d "$temporary_root/gift-panel-backup.XXXXXXXX")"
checkpoint_temp=''
cleanup() {
  local status=$?
  trap - EXIT
  if [[ -n "$checkpoint_temp" && -f "$checkpoint_temp" && "$checkpoint_temp" == "$backup_state_root/."*".next."* ]]; then
    rm -- "$checkpoint_temp"
  fi
  if [[ -n "${work_dir:-}" && -d "$work_dir" && "$work_dir" == "$temporary_root/gift-panel-backup."* ]]; then
    find "$work_dir" -depth -mindepth 1 -delete
    rmdir -- "$work_dir"
  fi
  exit "$status"
}
trap 'cleanup' EXIT

run_external() {
  "$timeout_bin" --foreground "$external_timeout" "$@"
}

calendar_value="$("$date_bin" -u '+%Y%m%dT%H%M%SZ %u %d %F')"
read -r timestamp weekday month_day today <<<"$calendar_value"
[[ "$timestamp" =~ ^[0-9]{8}T[0-9]{6}Z$ && "$weekday" =~ ^[1-7]$ && "$month_day" =~ ^[0-9]{2}$ && "$today" =~ ^[0-9]{4}-[0-9]{2}-[0-9]{2}$ ]] || exit 4
if [[ "$weekday" == 7 ]]; then
  weekly_due="$today"
else
  weekly_due="$("$date_bin" -u -d "$today - $weekday days" +%F)"
fi
monthly_due="${today:0:7}-01"

sql_file="$work_dir/gift-panel-${timestamp}.sql"
compressed_file="$sql_file.zst"
encrypted_payload="$compressed_file.age"
run_external "$docker_bin" compose -f "$compose_file" exec -T mysql sh -euc '
  MYSQL_PWD="$(cat "$MYSQL_ROOT_PASSWORD_FILE")"
  export MYSQL_PWD
  exec mysqldump --user=root --single-transaction --quick --routines --events --hex-blob "$MYSQL_DATABASE"
' >"$sql_file"
run_external "$zstd_bin" --quiet -T0 --ultra -19 -o "$compressed_file" "$sql_file"
run_external "$age_bin" --encrypt --recipients-file "$recipient_file" --output "$encrypted_payload" "$compressed_file"

advance_day() {
  local day=$1 cadence=$2
  case "$cadence" in
    daily) "$date_bin" -u -d "$day + 1 day" +%F ;;
    weekly) "$date_bin" -u -d "$day + 7 days" +%F ;;
    monthly) "$date_bin" -u -d "$day + 1 month" +%F ;;
    *) return 4 ;;
  esac
}

upload_period() {
  local period=$1 scheduled_day=$2
  local day_compact random_suffix base_name artifact_file checksum_file completion_file remote
  day_compact="${scheduled_day//-/}"
  random_suffix="$(od -An -N8 -tx1 /dev/urandom | tr -d ' \n')" || return
  [[ "$random_suffix" =~ ^[0-9a-f]{16}$ ]] || return 4
  base_name="gift-panel-${day_compact}-${timestamp}-${random_suffix}.sql.zst.age"
  artifact_file="$work_dir/$base_name"
  checksum_file="$artifact_file.sha256"
  completion_file="$artifact_file.complete"
  cp -- "$encrypted_payload" "$artifact_file" || return
  (
    cd "$work_dir" && sha256sum "$base_name" >"$base_name.sha256"
  ) || return
  printf 'artifact=%s\nperiod=%s\nscheduled_utc=%s\ndelivery=at-least-once\ncompleted_utc=%s\n' \
    "$base_name" "$period" "$scheduled_day" "$timestamp" >"$completion_file" || return
  remote="cos://$cos_bucket/hosted/backups/$period/$base_name"
  run_external "$cos_bin" cp "$artifact_file" "$remote" --config-path "$cos_config" --endpoint cos.ap-hongkong.myqcloud.com --disable-log --process-log=false --forbid-overwrite || return
  run_external "$cos_bin" cp "$checksum_file" "$remote.sha256" --config-path "$cos_config" --endpoint cos.ap-hongkong.myqcloud.com --disable-log --process-log=false --forbid-overwrite || return
  run_external "$cos_bin" cp "$completion_file" "$remote.complete" --config-path "$cos_config" --endpoint cos.ap-hongkong.myqcloud.com --disable-log --process-log=false --forbid-overwrite || return
  rm -- "$artifact_file" "$checksum_file" "$completion_file" || return
  printf 'backup_uploaded=%s period=%s scheduled_utc=%s\n' "$base_name" "$period" "$scheduled_day"
}

process_period() {
  local period=$1 current_due=$2
  local checkpoint_file="$backup_state_root/$period.next"
  local scheduled_day next_day earliest_day retention_days latest_next_day
  [[ ! -e "$checkpoint_file" || ( -f "$checkpoint_file" && ! -L "$checkpoint_file" ) ]] || return 4
  if [[ -e "$checkpoint_file" ]]; then
    IFS= read -r scheduled_day <"$checkpoint_file" || return
    [[ "$scheduled_day" =~ ^[0-9]{4}-[0-9]{2}-[0-9]{2}$ ]] || return 4
  else
    scheduled_day="$current_due"
  fi
  case "$period" in
    daily) retention_days=7 ;;
    weekly) retention_days=28 ;;
    monthly) retention_days=183 ;;
    *) return 4 ;;
  esac
  earliest_day="$("$date_bin" -u -d "$current_due - $((retention_days - 1)) days" +%F)" || return
  latest_next_day="$(advance_day "$current_due" "$period")" || return
  [[ "$earliest_day" =~ ^[0-9]{4}-[0-9]{2}-[0-9]{2}$ && "$latest_next_day" =~ ^[0-9]{4}-[0-9]{2}-[0-9]{2}$ ]] || return 4
  if [[ "$scheduled_day" < "$earliest_day" || "$scheduled_day" > "$latest_next_day" ]]; then
    printf 'backup checkpoint is outside retained period: cadence=%s next=%s\n' "$period" "$scheduled_day" >&2
    return 5
  fi
  if [[ "$period" == weekly ]]; then
    [[ "$("$date_bin" -u -d "$scheduled_day" +%u)" == 7 ]] || {
      printf 'backup checkpoint is off cadence: cadence=weekly next=%s\n' "$scheduled_day" >&2
      return 5
    }
  elif [[ "$period" == monthly && "$scheduled_day" != ????-??-01 ]]; then
    printf 'backup checkpoint is off cadence: cadence=monthly next=%s\n' "$scheduled_day" >&2
    return 5
  fi
  while [[ "$scheduled_day" < "$current_due" || "$scheduled_day" == "$current_due" ]]; do
    upload_period "$period" "$scheduled_day" || return
    next_day="$(advance_day "$scheduled_day" "$period")" || return
    [[ "$next_day" =~ ^[0-9]{4}-[0-9]{2}-[0-9]{2}$ ]] || return 4
    checkpoint_temp="$backup_state_root/.$period.next.$BASHPID"
    printf '%s\n' "$next_day" >"$checkpoint_temp" || return
    chmod 0600 -- "$checkpoint_temp" || return
    mv -f -- "$checkpoint_temp" "$checkpoint_file" || return
    checkpoint_temp=''
    scheduled_day="$next_day"
  done
}

overall_status=0
process_period daily "$today" || overall_status=1
process_period weekly "$weekly_due" || overall_status=1
process_period monthly "$monthly_due" || overall_status=1
exit "$overall_status"
