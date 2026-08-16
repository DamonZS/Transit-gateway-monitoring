#!/usr/bin/env bash
set -Eeuo pipefail

deploy_dir=${1:?deployment directory is required}
revision=${2:?revision is required}
incoming_dir="$deploy_dir/.incoming/$revision"
compose_name=docker-compose.production.yml
phase=preflight
lock_acquired=false
state_captured=false
backup_created=false
backup_complete=false
was_running=false
backup_dir=
next_compose=()

had_previous_env=false
had_previous_compose=false
had_previous_data=false

prune_backups() {
  local backup_name

  [[ -d "$deploy_dir/backups" ]] || return 0
  while IFS= read -r backup_name; do
    if [[ "$backup_name" =~ ^[0-9]{8}T[0-9]{6}Z-[0-9a-f]{40}$ ]]; then
      rm -rf "$deploy_dir/backups/$backup_name"
    fi
  done < <(
    find "$deploy_dir/backups" -mindepth 1 -maxdepth 1 -type d -printf '%f\n' |
      sort -r |
      tail -n +6
  )
}

restore_previous_metadata() {
  if [[ "$had_previous_env" == true ]]; then
    install -m 600 "$backup_dir/.env" "$deploy_dir/.env"
  else
    rm -f "$deploy_dir/.env"
  fi

  if [[ "$had_previous_compose" == true ]]; then
    install -m 600 "$backup_dir/$compose_name" "$deploy_dir/$compose_name"
  else
    rm -f "$deploy_dir/$compose_name"
  fi
}

restart_previous_service() {
  if [[ "$was_running" == true &&
        "$had_previous_env" == true &&
        "$had_previous_compose" == true ]]; then
    local previous_compose=(
      docker compose --env-file "$deploy_dir/.env" -f "$deploy_dir/$compose_name"
    )
    "${previous_compose[@]}" up -d --no-build --wait --wait-timeout 120 app ||
      echo "failed to restart the previous deployment" >&2
  fi
}

rollback_transaction() {
  echo "deployment failed during $phase; rolling back" >&2

  if [[ "$phase" == starting ]]; then
    if ((${#next_compose[@]} > 0)); then
      "${next_compose[@]}" logs --no-color --tail 200 app >&2 || true
      "${next_compose[@]}" down --remove-orphans || true
    fi

    rm -rf "$deploy_dir/data"
    if [[ "$had_previous_data" == true ]]; then
      tar -C "$deploy_dir" -xzf "$backup_dir/data.tar.gz" ||
        echo "failed to restore the previous data directory" >&2
    fi
  elif [[ "$state_captured" == true && "$had_previous_data" == false ]]; then
    rm -rf "$deploy_dir/data"
  fi

  case "$phase" in
    promotion|starting)
      restore_previous_metadata || echo "failed to restore deployment metadata" >&2
      ;;
  esac

  case "$phase" in
    backup|staging|promotion|starting)
      restart_previous_service
      ;;
  esac

  if [[ "$backup_created" == true && "$backup_complete" != true ]]; then
    rm -rf "$backup_dir"
  fi
}

finish_deployment() {
  local status=$?
  trap - EXIT INT TERM
  set +e

  if ((status != 0)); then
    rollback_transaction
  fi

  if [[ "$lock_acquired" == true ]]; then
    rm -f "$deploy_dir/.env.next" "$deploy_dir/$compose_name.next"
    prune_backups
  fi
  rm -rf "$incoming_dir"

  exit "$status"
}

trap finish_deployment EXIT
trap 'exit 130' INT
trap 'exit 143' TERM

install -d -m 700 "$deploy_dir" "$deploy_dir/backups"
exec 9>"$deploy_dir/.deploy.lock"
flock -w 120 9
lock_acquired=true

[[ -f "$deploy_dir/.env" ]] && had_previous_env=true
[[ -f "$deploy_dir/$compose_name" ]] && had_previous_compose=true
[[ -d "$deploy_dir/data" ]] && had_previous_data=true
state_captured=true

if [[ ! -f "$incoming_dir/$compose_name" || ! -f "$incoming_dir/.env" ]]; then
  echo "deployment payload is incomplete" >&2
  exit 1
fi

install -d -m 700 "$deploy_dir/data"
chmod 600 "$incoming_dir/.env"

incoming_compose=(docker compose --env-file "$incoming_dir/.env" -f "$incoming_dir/$compose_name")
"${incoming_compose[@]}" config -q

image=$(sed -n 's/^UPSTREAM_OPS_IMAGE=//p' "$incoming_dir/.env" | tail -n 1)
if [[ -z "$image" || "$image" != *@sha256:* ]]; then
  echo "UPSTREAM_OPS_IMAGE must use an immutable digest" >&2
  exit 1
fi
docker pull "$image"

timestamp=$(date -u +%Y%m%dT%H%M%SZ)
backup_dir="$deploy_dir/backups/$timestamp-$revision"
phase=backup

if [[ "$had_previous_env" == true ||
      "$had_previous_compose" == true ||
      "$had_previous_data" == true ]]; then
  install -d -m 700 "$backup_dir"
  backup_created=true

  if [[ "$had_previous_compose" == true ]]; then
    cp "$deploy_dir/$compose_name" "$backup_dir/$compose_name"
  fi
  if [[ "$had_previous_env" == true ]]; then
    cp "$deploy_dir/.env" "$backup_dir/.env"
    chmod 600 "$backup_dir/.env"
  fi

  if [[ "$had_previous_env" == true && "$had_previous_compose" == true ]]; then
    current_compose=(docker compose --env-file "$deploy_dir/.env" -f "$deploy_dir/$compose_name")
    if docker inspect -f '{{.State.Running}}' upstream-ops 2>/dev/null | grep -qx true; then
      was_running=true
      "${current_compose[@]}" stop app
    fi
  fi

  if [[ "$had_previous_data" == true ]]; then
    tar -C "$deploy_dir" -czf "$backup_dir/data.tar.gz" data
  fi
  backup_complete=true
fi

phase=staging
install -m 600 "$incoming_dir/.env" "$deploy_dir/.env.next"
install -m 600 "$incoming_dir/$compose_name" "$deploy_dir/$compose_name.next"

phase=promotion
mv -f "$deploy_dir/.env.next" "$deploy_dir/.env"
mv -f "$deploy_dir/$compose_name.next" "$deploy_dir/$compose_name"

next_compose=(docker compose --env-file "$deploy_dir/.env" -f "$deploy_dir/$compose_name")
phase=starting
if ! "${next_compose[@]}" up -d --no-build --wait --wait-timeout 120 app; then
  exit 1
elif ! curl -fsS http://127.0.0.1:8418/healthz >/dev/null; then
  exit 1
else
  running_image=$(docker inspect -f '{{.Config.Image}}' upstream-ops)
  if [[ "$running_image" != "$image" ]]; then
    echo "running image mismatch: $running_image" >&2
    exit 1
  fi
fi

echo "deployed $revision using $image"
phase=complete
