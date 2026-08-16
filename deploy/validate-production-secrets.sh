#!/usr/bin/env bash
set -euo pipefail

for name in APP_SECRET ADMIN_PASSWORD AUTH_TOKEN_SECRET SSO_SHARED_SECRET; do
  value=${!name:-}
  if [[ ! "$value" =~ ^[0-9a-f]{64}$ ]]; then
    echo "$name must contain exactly 64 lowercase hexadecimal characters" >&2
    exit 1
  fi
done
