#!/usr/bin/env bash
set -euo pipefail
umask 077

readonly cos_bin="${HOSTED_COS_BIN:-coscli}"
readonly zstd_bin="${HOSTED_ZSTD_BIN:-zstd}"
readonly age_bin="${HOSTED_AGE_BIN:-age}"
readonly timeout_bin="${HOSTED_TIMEOUT_BIN:-timeout}"
readonly external_timeout="${HOSTED_EXTERNAL_TIMEOUT:-10m}"
readonly date_bin="${HOSTED_DATE_BIN:-date}"
readonly cos_config="${HOSTED_COS_CONFIG_FILE:?set HOSTED_COS_CONFIG_FILE}"
readonly cos_bucket="${HOSTED_COS_BUCKET:?set HOSTED_COS_BUCKET}"
readonly recipient_file="${HOSTED_AGE_RECIPIENT_FILE:?set HOSTED_AGE_RECIPIENT_FILE}"
readonly nginx_log_root="${HOSTED_NGINX_LOG_ROOT:-/var/log/nginx}"
readonly app_log_root="${HOSTED_APP_LOG_ROOT:-/var/log/gift-panel-hosted}"
readonly archive_state_root="${HOSTED_ARCHIVE_STATE_ROOT:-/var/lib/gift-panel-hosted-log-archive}"
readonly checkpoint_file="$archive_state_root/next-day"
readonly lock_file="${HOSTED_ARCHIVE_LOCK_FILE:-/run/lock/gift-panel-hosted-archive.lock}"
readonly temporary_root="${TMPDIR:-/tmp}"

[[ "${HOSTED_COS_REGION:?set HOSTED_COS_REGION}" == "ap-hongkong" ]] || exit 2
[[ "$external_timeout" =~ ^[1-9][0-9]*(s|m|h)$ ]] || exit 2
[[ "$cos_bucket" =~ ^[a-z0-9][a-z0-9-]*-[0-9]+$ ]] || exit 2
[[ -r "$cos_config" && -r "$recipient_file" && -d "$nginx_log_root" && ! -L "$nginx_log_root" ]] || exit 2
[[ -d "$app_log_root" && ! -L "$app_log_root" && -r "$app_log_root" ]] || {
  printf '%s\n' 'application log directory is unavailable' >&2
  exit 2
}
[[ -d "$archive_state_root" && ! -L "$archive_state_root" && -w "$archive_state_root" ]] || {
  printf '%s\n' 'archive state directory is unavailable' >&2
  exit 2
}
[[ ! -e "$checkpoint_file" || ( -f "$checkpoint_file" && ! -L "$checkpoint_file" ) ]] || {
  printf '%s\n' 'archive checkpoint is unsafe' >&2
  exit 2
}

mkdir -p -- "$(dirname -- "$lock_file")"
exec 9>"$lock_file"
flock -n 9 || exit 3

work_dir="$(mktemp -d "$temporary_root/gift-panel-log-archive.XXXXXXXX")"
checkpoint_temp=''
cleanup() {
  local status=$?
  trap - EXIT
  if [[ -n "$checkpoint_temp" && -f "$checkpoint_temp" && "$checkpoint_temp" == "$archive_state_root/.next-day."* ]]; then
    rm -- "$checkpoint_temp"
  fi
  if [[ -n "${work_dir:-}" && -d "$work_dir" && "$work_dir" == "$temporary_root/gift-panel-log-archive."* ]]; then
    find "$work_dir" -depth -mindepth 1 -delete
    rmdir -- "$work_dir"
  fi
  exit "$status"
}
trap 'cleanup' EXIT

run_external() {
  "$timeout_bin" --foreground "$external_timeout" "$@"
}

calendar_value="$("$date_bin" -u '+%Y%m%dT%H%M%SZ %s')"
read -r timestamp now_epoch <<<"$calendar_value"
[[ "$timestamp" =~ ^[0-9]{8}T[0-9]{6}Z$ && "$now_epoch" =~ ^[0-9]+$ ]] || exit 4
host_timezone_observed="$("$date_bin" '+%Z %z')"
[[ "$host_timezone_observed" =~ ^[A-Za-z0-9_+/-]+\ [+-][0-9]{4}$ ]] || exit 4
earliest_epoch="$((now_epoch - 34 * 86400))"
target_epoch="$((now_epoch - 31 * 86400))"
earliest_day="$("$date_bin" -u -d "@$earliest_epoch" +%F)"
target_day="$("$date_bin" -u -d "@$target_epoch" +%F)"
latest_next_day="$("$date_bin" -u -d "$target_day + 1 day" +%F)"
[[ "$earliest_day" =~ ^[0-9]{4}-[0-9]{2}-[0-9]{2}$ && "$target_day" =~ ^[0-9]{4}-[0-9]{2}-[0-9]{2}$ ]] || exit 4

if [[ -e "$checkpoint_file" ]]; then
  IFS= read -r archive_day <"$checkpoint_file"
  [[ "$archive_day" =~ ^[0-9]{4}-[0-9]{2}-[0-9]{2}$ ]] || {
    printf '%s\n' 'archive checkpoint is invalid' >&2
    exit 5
  }
else
  archive_day=''
  while IFS= read -r candidate_day; do
    [[ "$candidate_day" =~ ^[0-9]{4}-[0-9]{2}-[0-9]{2}$ ]] || exit 5
    if [[ "$candidate_day" < "$latest_next_day" ]]; then
      archive_day="$candidate_day"
      break
    fi
  done < <(
    find "$app_log_root" "$nginx_log_root" -xdev -maxdepth 1 -type f -mmin +43200 \
      \( -name 'app.log-????????.gz' -o -name 'gift-panel-hosted.access.log-????????.gz' -o -name 'gift-panel-hosted.error.log-????????.gz' \) \
      -printf '%f\n' \
      | sed -n -E 's/^.*-([0-9]{4})([0-9]{2})([0-9]{2})\.gz$/\1-\2-\3/p' \
      | sort -u
  )
  [[ -n "$archive_day" ]] || exit 0
fi
if [[ "$archive_day" < "$earliest_day" ]]; then
  printf 'archive checkpoint is older than retained logs: next=%s earliest=%s\n' "$archive_day" "$earliest_day" >&2
  exit 5
fi
if [[ "$archive_day" > "$latest_next_day" ]]; then
  printf '%s\n' 'archive checkpoint is ahead of the closed-log window' >&2
  exit 5
fi

archive_one_day() {
  local day=$1
  local next_day day_compact random_suffix base_name day_work file_list manifest_file
  local tar_file compressed_file artifact_file checksum_file completion_file
  local file_count range_start range_end source_file
  local modified_epoch byte_count content_hash remote

  next_day="$("$date_bin" -u -d "$day + 1 day" +%F)"
  [[ "$next_day" =~ ^[0-9]{4}-[0-9]{2}-[0-9]{2}$ ]] || return 4
  day_compact="${day//-/}"
  random_suffix="$(od -An -N8 -tx1 /dev/urandom | tr -d ' \n')"
  [[ "$random_suffix" =~ ^[0-9a-f]{16}$ ]] || return 4
  base_name="gift-panel-logs-${day_compact}-${timestamp}-${random_suffix}.tar.zst.age"
  day_work="$work_dir/$day"
  mkdir -- "$day_work"
  file_list="$day_work/files.list"
  manifest_file="$day_work/manifest.tsv"
  tar_file="$day_work/${base_name%.zst.age}"
  compressed_file="$day_work/${base_name%.age}"
  artifact_file="$day_work/$base_name"
  checksum_file="$artifact_file.sha256"
  completion_file="$artifact_file.complete"
  find "$nginx_log_root" -xdev -maxdepth 1 -type f -mmin +43200 \
    \( -name "gift-panel-hosted.access.log-${day_compact}.gz" \
       -o -name "gift-panel-hosted.error.log-${day_compact}.gz" \) \
    -print0 | sort -zu >"$file_list"
  find "$app_log_root" -xdev -maxdepth 1 -type f -mmin +43200 \
    -name "app.log-${day_compact}.gz" -print0 | sort -zu >>"$file_list"

  file_count=0
  range_start='none'
  range_end='none'
  printf 'app_rotation_date_host_local\t%s\nnginx_rotation_date_host_local\t%s\nhost_timezone_observed\t%s\ndelivery\tat-least-once\ncrash_window\tcompleted-object-may-repeat-before-local-checkpoint-no-data-loss\ncreated_utc\t%s\n' \
    "$day" "$day" "$host_timezone_observed" "$timestamp" >"$manifest_file"
  while IFS= read -r -d '' source_file; do
    modified_epoch="$(stat --format='%Y' -- "$source_file")"
    byte_count="$(stat --format='%s' -- "$source_file")"
    content_hash="$(sha256sum -- "$source_file" | awk '{print $1}')"
    printf 'file\t%s\t%s\t%s\t%s\n' "$source_file" "$modified_epoch" "$byte_count" "$content_hash" >>"$manifest_file"
    ((file_count += 1))
    [[ "$range_start" == none || "$modified_epoch" -lt "$range_start" ]] && range_start="$modified_epoch"
    [[ "$range_end" == none || "$modified_epoch" -gt "$range_end" ]] && range_end="$modified_epoch"
  done <"$file_list"
  if (( file_count != 3 )); then
    printf 'closed hosted rotation set is incomplete: date=%s files=%s want=3\n' "$day" "$file_count" >&2
    return 5
  fi
  printf 'summary\tfile_count=%s\trange_start_epoch=%s\trange_end_epoch=%s\n' "$file_count" "$range_start" "$range_end" >>"$manifest_file"

  tar --create --file "$tar_file" -C "$day_work" manifest.tsv --null --verbatim-files-from --files-from="$file_list"
  run_external "$zstd_bin" --quiet -T0 --ultra -19 -o "$compressed_file" "$tar_file"
  run_external "$age_bin" --encrypt --recipients-file "$recipient_file" --output "$artifact_file" "$compressed_file"
  (
    cd "$day_work"
    sha256sum "$base_name" >"$base_name.sha256"
  )
  printf 'artifact=%s\napp_rotation_date_host_local=%s\nnginx_rotation_date_host_local=%s\ndelivery=at-least-once\ncompleted_utc=%s\n' \
    "$base_name" "$day" "$day" "$timestamp" >"$completion_file"

  remote="cos://$cos_bucket/hosted/logs/$base_name"
  run_external "$cos_bin" cp "$artifact_file" "$remote" --config-path "$cos_config" --endpoint cos.ap-hongkong.myqcloud.com --disable-log --process-log=false --forbid-overwrite
  run_external "$cos_bin" cp "$checksum_file" "$remote.sha256" --config-path "$cos_config" --endpoint cos.ap-hongkong.myqcloud.com --disable-log --process-log=false --forbid-overwrite
  run_external "$cos_bin" cp "$completion_file" "$remote.complete" --config-path "$cos_config" --endpoint cos.ap-hongkong.myqcloud.com --disable-log --process-log=false --forbid-overwrite
  printf 'log_archive_uploaded=%s day=%s files=%s\n' "$base_name" "$day" "$file_count"
}

while [[ "$archive_day" < "$latest_next_day" ]]; do
  archive_one_day "$archive_day"
  next_day="$("$date_bin" -u -d "$archive_day + 1 day" +%F)"
  checkpoint_temp="$archive_state_root/.next-day.$BASHPID"
  printf '%s\n' "$next_day" >"$checkpoint_temp"
  chmod 0600 -- "$checkpoint_temp"
  mv -f -- "$checkpoint_temp" "$checkpoint_file"
  checkpoint_temp=''
  archive_day="$next_day"
done
