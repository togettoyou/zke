#!/bin/sh
set -eu

# Supervises PostgreSQL, the metrics storage and ZKE Server.
#
# Order matters: the Server runs migrations before it listens, and it must not
# offer ingest before there is somewhere to write. Any one of the three exiting
# brings the container down — a half-running ZKE still answers the Console and
# still holds its Agents, so the part that stopped would go unnoticed.

config_file="${ZKE_CONFIG_FILE:-/etc/zke/zke-server.yaml}"
metrics_data_path="${ZKE_METRICS_STORAGE_DATA_PATH:-/var/lib/victoria-metrics}"
metrics_listen_address="${ZKE_METRICS_LISTEN_ADDRESS:-127.0.0.1:8428}"
metrics_retention_period="${ZKE_METRICS_RETENTION_PERIOD:-1}"
metrics_enabled="${ZKE_OBSERVABILITY_METRICS_ENABLED:-true}"

mkdir -p /data "$metrics_data_path"

/usr/local/bin/docker-entrypoint.sh postgres &
postgres_pid=$!
metrics_pid=""
server_pid=""

shutdown() {
    for pid in "$server_pid" "$metrics_pid" "$postgres_pid"; do
        if [ -n "$pid" ] && kill -0 "$pid" 2>/dev/null; then
            kill -TERM "$pid" 2>/dev/null || true
        fi
    done
}
trap shutdown INT TERM

# alive reports whether every process that was started is still running. An
# empty pid means that process was deliberately not started.
alive() {
    for pid in "$postgres_pid" "$metrics_pid" "$server_pid"; do
        if [ -n "$pid" ] && ! kill -0 "$pid" 2>/dev/null; then
            return 1
        fi
    done
    return 0
}

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

# Storage follows the Server's own switch rather than a second one: with
# metrics off nothing would ever write to it.
if [ "$metrics_enabled" != "false" ]; then
    /usr/local/bin/victoria-metrics \
        -storageDataPath="$metrics_data_path" \
        -httpListenAddr="$metrics_listen_address" \
        -retentionPeriod="$metrics_retention_period" &
    metrics_pid=$!

    attempt=0
    until wget -qO- "http://127.0.0.1:8428/health" >/dev/null 2>&1; do
        if ! kill -0 "$metrics_pid" 2>/dev/null; then
            wait "$metrics_pid"
            exit $?
        fi
        attempt=$((attempt + 1))
        if [ "$attempt" -ge 60 ]; then
            echo "metrics storage did not become ready within 60 seconds" >&2
            shutdown
            wait "$metrics_pid" 2>/dev/null || true
            wait "$postgres_pid" 2>/dev/null || true
            exit 1
        fi
        sleep 1
    done
fi

/usr/local/bin/zke-server --config "$config_file" &
server_pid=$!

while alive; do
    sleep 1
done

# The Server's status is the container's whenever the Server is what stopped.
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
wait "$metrics_pid" 2>/dev/null || true
wait "$postgres_pid" 2>/dev/null || true
exit "$status"
