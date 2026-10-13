#!/bin/sh
set -eu
# Lite upstream accepts environment variables, not *_FILE.
export ADMIN_PASSWORD="$(cat /run/secrets/admin_password)"
export API_TOKEN_HMAC_SECRET="$(cat /run/secrets/hmac_secret)"
export DSN="postgres://lite:$(cat /run/secrets/postgres_password)@postgres:5432/lite?sslmode=disable"
export CLICKHOUSE_PASSWORD="$(cat /run/secrets/clickhouse_password)"
exec /usr/local/bin/lite
