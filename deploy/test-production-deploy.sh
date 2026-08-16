#!/usr/bin/env bash
set -uo pipefail

script_dir=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)
deploy_script="$script_dir/production-deploy.sh"
secret_validator="$script_dir/validate-production-secrets.sh"
fixture=
failures=0

cleanup_fixture() {
  if [[ -n "${fixture:-}" ]]; then
    rm -rf "$fixture"
    fixture=
  fi
}
trap cleanup_fixture EXIT

fail() {
  echo "    $*" >&2
  return 1
}

assert_file_equals() {
  local path=$1
  local expected=$2
  [[ -f "$path" ]] || fail "expected file to exist: $path" || return 1
  [[ "$(<"$path")" == "$expected" ]] ||
    fail "unexpected contents in $path" || return 1
}

assert_absent() {
  local path=$1
  [[ ! -e "$path" ]] || fail "expected path to be absent: $path" || return 1
}

new_fixture() {
  cleanup_fixture
  fixture=$(mktemp -d)
  deploy_dir="$fixture/deploy"
  fake_bin="$fixture/bin"
  docker_log="$fixture/docker.log"
  mkdir -p "$deploy_dir" "$fake_bin"
  : > "$docker_log"

  cat > "$fake_bin/docker" <<'EOF'
#!/usr/bin/env bash
set -u
printf '%s\n' "$*" >> "${DOCKER_LOG:?}"

if [[ "${1:-}" == pull ]]; then
  exit 0
fi

if [[ "${1:-}" == inspect ]]; then
  if [[ "$*" == *State.Running* ]]; then
    printf '%s\n' "${PREVIOUS_RUNNING:-false}"
  else
    printf '%s\n' "${EXPECTED_IMAGE:?}"
  fi
  exit 0
fi

if [[ "${1:-}" == compose ]]; then
  case " $* " in
    *" config -q "*|*" stop app "*|*" logs "*|*" down "*)
      exit 0
      ;;
    *" up -d "*)
      if [[ "${MUTATE_DATA_ON_UP:-false}" == true ]]; then
        mkdir -p "${TEST_DEPLOY_DIR:?}/data"
        printf 'new-db\n' > "${TEST_DEPLOY_DIR:?}/data/upstream-ops.db"
        printf 'generated\n' > "${TEST_DEPLOY_DIR:?}/data/generated-during-startup"
      fi
      if [[ "${FAIL_UP:-false}" == true ]]; then
        exit 1
      fi
      exit 0
      ;;
  esac
fi

echo "unexpected docker invocation: $*" >&2
exit 64
EOF

  cat > "$fake_bin/install" <<'EOF'
#!/usr/bin/env bash
set -u
destination=${!#}
if [[ "${FAIL_INSTALL_NEXT:-false}" == true && "$destination" == *.env.next ]]; then
  echo "injected pre-promotion install failure" >&2
  exit 70
fi
if [[ "${1:-}" == -d ]]; then
  shift
  if [[ "${1:-}" == -m ]]; then
    shift 2
  fi
  mkdir -p "$@"
  exit 0
fi
if [[ "${1:-}" == -m ]]; then
  shift 2
fi
cp "$1" "$2"
EOF

  cat > "$fake_bin/date" <<'EOF'
#!/usr/bin/env bash
printf '%s\n' 20260817T120000Z
EOF

  cat > "$fake_bin/flock" <<'EOF'
#!/usr/bin/env bash
if [[ "${STATE_ON_LOCK:-false}" == true ]]; then
  mkdir -p "${TEST_DEPLOY_DIR:?}/data"
  printf 'old-db\n' > "${TEST_DEPLOY_DIR:?}/data/upstream-ops.db"
  printf 'old-compose\n' > "${TEST_DEPLOY_DIR:?}/docker-compose.production.yml"
  printf 'UPSTREAM_OPS_IMAGE=ghcr.io/example/old@sha256:%s\n' \
    "$(printf 'c%.0s' {1..64})" > "${TEST_DEPLOY_DIR:?}/.env"
fi
exit 0
EOF

  chmod +x "$fake_bin/docker" "$fake_bin/install" "$fake_bin/date" "$fake_bin/flock"
}

write_payload() {
  local revision=$1
  incoming_dir="$deploy_dir/.incoming/$revision"
  expected_image="ghcr.io/example/upstream-ops@sha256:$(printf 'a%.0s' {1..64})"
  mkdir -p "$incoming_dir"
  printf 'new-compose\n' > "$incoming_dir/docker-compose.production.yml"
  printf 'UPSTREAM_OPS_IMAGE=%s\n' "$expected_image" > "$incoming_dir/.env"
}

run_deploy_expect_failure() {
  env \
    PATH="$fake_bin:/usr/bin:/bin" \
    DOCKER_LOG="$docker_log" \
    EXPECTED_IMAGE="$expected_image" \
    TEST_DEPLOY_DIR="$deploy_dir" \
    PREVIOUS_RUNNING="${PREVIOUS_RUNNING:-false}" \
    FAIL_INSTALL_NEXT="${FAIL_INSTALL_NEXT:-false}" \
    FAIL_UP="${FAIL_UP:-false}" \
    MUTATE_DATA_ON_UP="${MUTATE_DATA_ON_UP:-false}" \
    STATE_ON_LOCK="${STATE_ON_LOCK:-false}" \
    bash "$deploy_script" "$deploy_dir" "$revision" > "$fixture/deploy.out" 2>&1
  local status=$?
  [[ $status -ne 0 ]] || fail "deployment unexpectedly succeeded" || return 1
}

seed_stale_backups() {
  local suffix
  suffix=$(printf 'b%.0s' {1..40})
  mkdir -p "$deploy_dir/backups"
  for day in 01 02 03 04 05 06; do
    mkdir "$deploy_dir/backups/202608${day}T120000Z-$suffix"
  done
}

test_pre_promotion_failure_rolls_back_and_cleans() {
  new_fixture
  revision=$(printf '1%.0s' {1..40})
  write_payload "$revision"
  mkdir -p "$deploy_dir/data"
  printf 'old-compose\n' > "$deploy_dir/docker-compose.production.yml"
  printf 'UPSTREAM_OPS_IMAGE=ghcr.io/example/old@sha256:%s\n' "$(printf 'c%.0s' {1..64})" > "$deploy_dir/.env"
  printf 'old-db\n' > "$deploy_dir/data/upstream-ops.db"
  seed_stale_backups

  PREVIOUS_RUNNING=true FAIL_INSTALL_NEXT=true FAIL_UP=false MUTATE_DATA_ON_UP=false \
    run_deploy_expect_failure || return 1

  grep -q 'stop app' "$docker_log" || fail "previous container was not stopped by the fixture" || return 1
  grep -q 'up -d --no-build --wait --wait-timeout 120 app' "$docker_log" ||
    fail "previous container was not restarted after the injected failure" || return 1
  assert_file_equals "$deploy_dir/docker-compose.production.yml" old-compose || return 1
  grep -q 'ghcr.io/example/old@sha256:' "$deploy_dir/.env" ||
    fail "previous environment was not preserved" || return 1
  assert_file_equals "$deploy_dir/data/upstream-ops.db" old-db || return 1
  assert_absent "$incoming_dir" || return 1
  assert_absent "$deploy_dir/.env.next" || return 1

  local backup_count
  backup_count=$(find "$deploy_dir/backups" -mindepth 1 -maxdepth 1 -type d | wc -l)
  [[ $backup_count -eq 5 ]] || fail "expected 5 retained backups, found $backup_count" || return 1
}

test_database_only_installation_is_backed_up_and_restored() {
  new_fixture
  revision=$(printf '2%.0s' {1..40})
  write_payload "$revision"
  mkdir -p "$deploy_dir/data"
  printf 'old-db\n' > "$deploy_dir/data/upstream-ops.db"

  PREVIOUS_RUNNING=false FAIL_INSTALL_NEXT=false FAIL_UP=true MUTATE_DATA_ON_UP=true \
    run_deploy_expect_failure || return 1

  assert_file_equals "$deploy_dir/data/upstream-ops.db" old-db || return 1
  assert_absent "$deploy_dir/data/generated-during-startup" || return 1
  assert_absent "$deploy_dir/docker-compose.production.yml" || return 1
  assert_absent "$deploy_dir/.env" || return 1
  assert_absent "$incoming_dir" || return 1
  [[ $(find "$deploy_dir/backups" -name data.tar.gz -type f | wc -l) -eq 1 ]] ||
    fail "database-only installation did not get a data backup" || return 1
}

test_state_is_snapshotted_after_lock_acquisition() {
  new_fixture
  revision=$(printf '4%.0s' {1..40})
  write_payload "$revision"

  PREVIOUS_RUNNING=true FAIL_INSTALL_NEXT=true FAIL_UP=false MUTATE_DATA_ON_UP=false \
    STATE_ON_LOCK=true run_deploy_expect_failure || return 1

  assert_file_equals "$deploy_dir/data/upstream-ops.db" old-db || return 1
  assert_file_equals "$deploy_dir/docker-compose.production.yml" old-compose || return 1
  grep -q 'stop app' "$docker_log" || fail "state created before lock release was not detected" || return 1
  grep -q 'up -d --no-build --wait --wait-timeout 120 app' "$docker_log" ||
    fail "locked previous deployment was not restarted" || return 1
}

test_fresh_failed_deployment_leaves_no_promoted_state() {
  new_fixture
  revision=$(printf '3%.0s' {1..40})
  write_payload "$revision"

  PREVIOUS_RUNNING=false FAIL_INSTALL_NEXT=false FAIL_UP=true MUTATE_DATA_ON_UP=true \
    run_deploy_expect_failure || return 1

  assert_absent "$deploy_dir/data" || return 1
  assert_absent "$deploy_dir/docker-compose.production.yml" || return 1
  assert_absent "$deploy_dir/.env" || return 1
  assert_absent "$incoming_dir" || return 1
}

test_secret_validator_accepts_safe_lowercase_hex() {
  [[ -f "$secret_validator" ]] || fail "missing secret validator: $secret_validator" || return 1
  local hex64
  hex64=$(printf 'a%.0s' {1..64})
  APP_SECRET=$hex64 \
    ADMIN_PASSWORD=$hex64 \
    AUTH_TOKEN_SECRET=$hex64 \
    SSO_SHARED_SECRET=$hex64 \
    bash "$secret_validator"
}

test_secret_validator_rejects_unsafe_values() {
  [[ -f "$secret_validator" ]] || fail "missing secret validator: $secret_validator" || return 1
  local hex64 short
  hex64=$(printf 'a%.0s' {1..64})
  short=$(printf 'a%.0s' {1..62})

  if APP_SECRET=$short ADMIN_PASSWORD=$hex64 AUTH_TOKEN_SECRET=$hex64 SSO_SHARED_SECRET=$hex64 \
    bash "$secret_validator" >/dev/null 2>&1; then
    fail "validator accepted an undersized APP_SECRET" || return 1
  fi
  if APP_SECRET=$hex64 ADMIN_PASSWORD=$short AUTH_TOKEN_SECRET=$hex64 SSO_SHARED_SECRET=$hex64 \
    bash "$secret_validator" >/dev/null 2>&1; then
    fail "validator accepted an undersized ADMIN_PASSWORD" || return 1
  fi
  if APP_SECRET=$hex64 ADMIN_PASSWORD=$hex64 AUTH_TOKEN_SECRET=$short SSO_SHARED_SECRET=$hex64 \
    bash "$secret_validator" >/dev/null 2>&1; then
    fail "validator accepted an undersized AUTH_TOKEN_SECRET" || return 1
  fi
  if APP_SECRET=$hex64 ADMIN_PASSWORD=$hex64 AUTH_TOKEN_SECRET=$hex64 SSO_SHARED_SECRET=$short \
    bash "$secret_validator" >/dev/null 2>&1; then
    fail "validator accepted an undersized SSO_SHARED_SECRET" || return 1
  fi
  if APP_SECRET=$hex64 ADMIN_PASSWORD="${hex64}=" AUTH_TOKEN_SECRET=$hex64 SSO_SHARED_SECRET=$hex64 \
    bash "$secret_validator" >/dev/null 2>&1; then
    fail "validator accepted dotenv metacharacters" || return 1
  fi
  if APP_SECRET=$hex64 ADMIN_PASSWORD=$hex64 AUTH_TOKEN_SECRET=${hex64^^} SSO_SHARED_SECRET=$hex64 \
    bash "$secret_validator" >/dev/null 2>&1; then
    fail "validator accepted uppercase hex" || return 1
  fi
}

run_test() {
  local name=$1
  shift
  if "$@"; then
    echo "ok - $name"
  else
    echo "not ok - $name"
    if [[ -n "${fixture:-}" && -s "$fixture/deploy.out" ]]; then
      sed 's/^/    deploy: /' "$fixture/deploy.out" >&2
    fi
    failures=$((failures + 1))
  fi
  cleanup_fixture
}

run_test "pre-promotion failure restores service and cleans transaction" \
  test_pre_promotion_failure_rolls_back_and_cleans
run_test "database-only installation is backed up and restored" \
  test_database_only_installation_is_backed_up_and_restored
run_test "deployment state is snapshotted after lock acquisition" \
  test_state_is_snapshotted_after_lock_acquisition
run_test "fresh failed deployment leaves no promoted state" \
  test_fresh_failed_deployment_leaves_no_promoted_state
run_test "secret validator accepts safe lowercase hex" \
  test_secret_validator_accepts_safe_lowercase_hex
run_test "secret validator rejects unsafe values" \
  test_secret_validator_rejects_unsafe_values

if ((failures > 0)); then
  echo "$failures test(s) failed" >&2
  exit 1
fi

echo "all deployment tests passed"
