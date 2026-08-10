#!/bin/sh
set -eu

config_file="${ZKE_CONFIG_FILE:-/etc/zke/zke-server.yaml}"
mkdir -p /var/lib/zke

/usr/local/bin/docker-entrypoint.sh postgres &
postgres_pid=$!
server_pid=""

shutdown() {
    if [ -n "$server_pid" ] && kill -0 "$server_pid" 2>/dev/null; then
        kill -TERM "$server_pid" 2>/dev/null || true
    fi
    if kill -0 "$postgres_pid" 2>/dev/null; then
        kill -TERM "$postgres_pid" 2>/dev/null || true
    fi
}
trap shutdown INT TERM

attempt=0
until pg_isready -h 127.0.0.1 -U "$POSTGRES_USER" -d "$POSTGRES_DB" >/dev/null 2>&1; do
    if ! kill -0 "$postgres_pid" 2>/dev/null; then
        wait "$postgres_pid"
        exit $?
    fi
    attempt=$((attempt + 1))
    if [ "$attempt" -ge 60 ]; then
        echo "PostgreSQL did not become ready within 60 seconds" >&2
        shutdown
        wait "$postgres_pid" 2>/dev/null || true
        exit 1
    fi
    sleep 1
done

/usr/local/bin/zke-server --config "$config_file" &
server_pid=$!

while kill -0 "$postgres_pid" 2>/dev/null && kill -0 "$server_pid" 2>/dev/null; do
    sleep 1
done

status=1
if ! kill -0 "$server_pid" 2>/dev/null; then
    if wait "$server_pid"; then
        status=0
    else
        status=$?
    fi
fi
shutdown
wait "$server_pid" 2>/dev/null || true
wait "$postgres_pid" 2>/dev/null || true
exit "$status"
