#!/bin/sh
# Start the owned Go control plane and its managed native backend.
set -eu
umask 077

root=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
state="$root/build/pilotllama"
api="$root/build/inferno-api"
backend="$root/build/grpc/inferno_rpc_server"
studio="$root/studio/dist"

model=${PILOTLLAMA_MODEL:-}
model_id=${PILOTLLAMA_MODEL_ID:-}
batching=${PILOTLLAMA_BATCHING:-}
listen=${PILOTLLAMA_LISTEN:-127.0.0.1:8080}
rate=${PILOTLLAMA_RATE_PER_MINUTE:-60}
burst=${PILOTLLAMA_RATE_BURST:-4}

usage() {
    echo "Usage: ./scripts/start.sh [--model ABSOLUTE_GGUF] [--model-id ID]" >&2
    echo "                          [--batching queue|continuous] [--listen 127.0.0.1:PORT]" >&2
}

while [ "$#" -gt 0 ]; do
    [ "$#" -ge 2 ] || { usage; exit 2; }
    case "$1" in
        --model) model=$2 ;;
        --model-id) model_id=$2 ;;
        --batching) batching=$2 ;;
        --listen) listen=$2 ;;
        *) usage; exit 2 ;;
    esac
    shift 2
done

if [ -L "$state" ]; then
    echo "Refusing symlinked runtime state directory: $state" >&2
    exit 1
fi
mkdir -p "$state"
chmod 700 "$state"

if [ -f "$state/service.pid" ]; then
    old_pid=$(cat "$state/service.pid" 2>/dev/null || true)
    case "$old_pid" in
        ''|*[!0-9]*) echo "Invalid existing PID state; run ./scripts/stop.sh and review $state" >&2; exit 1 ;;
        *)
            if kill -0 "$old_pid" 2>/dev/null; then
                old_command=$(ps -p "$old_pid" -o command= 2>/dev/null || true)
                case "$old_command" in
                    "$api"|"$api "*) echo "PilotLlama is already running with PID $old_pid" >&2 ;;
                    *) echo "PID state does not identify an owned PilotLlama process; refusing to start" >&2 ;;
                esac
                exit 1
            fi
            rm -f "$state/service.pid"
            ;;
    esac
fi

if [ -z "$model" ] && [ -f "$state/model.path" ]; then model=$(cat "$state/model.path"); fi
if [ -z "$model_id" ] && [ -f "$state/model.id" ]; then model_id=$(cat "$state/model.id"); fi
if [ -z "$batching" ] && [ -f "$state/batching" ]; then batching=$(cat "$state/batching"); fi
[ -n "$model_id" ] || model_id=local-model
[ -n "$batching" ] || batching=queue

case "$model" in
    /*) ;;
    '') echo "A model is required on first start: ./scripts/start.sh --model /absolute/path/model.gguf" >&2; exit 2 ;;
    *) echo "--model must be an absolute path" >&2; exit 2 ;;
esac
[ -f "$model" ] || { echo "Model is not an existing regular file: $model" >&2; exit 2; }
newline='
'
carriage_return=$(printf '\r')
case "$model" in *"$newline"*|*"$carriage_return"*) echo "Model path contains an unsupported line break" >&2; exit 2 ;; esac
case "$model_id" in
    ''|*[!abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789._-]*)
        echo "Model ID must use 1..128 ASCII letters, digits, dot, underscore or hyphen" >&2; exit 2 ;;
esac
[ "${#model_id}" -le 128 ] || { echo "Model ID exceeds 128 characters" >&2; exit 2; }
case "$batching" in queue|continuous) ;; *) echo "Batching must be queue or continuous" >&2; exit 2 ;; esac
case "$listen" in
    127.0.0.1:*) port=${listen#127.0.0.1:} ;;
    *) echo "Listen address must be an explicit IPv4 loopback address: 127.0.0.1:PORT" >&2; exit 2 ;;
esac
case "$port" in ''|*[!0-9]*) echo "Listen port must be an integer" >&2; exit 2 ;; esac
[ "$port" -ge 1 ] 2>/dev/null && [ "$port" -le 65535 ] || { echo "Listen port must be 1..65535" >&2; exit 2; }
case "$rate" in ''|*[!0-9]*) echo "PILOTLLAMA_RATE_PER_MINUTE must be 0..60000" >&2; exit 2 ;; esac
case "$burst" in ''|*[!0-9]*) echo "PILOTLLAMA_RATE_BURST must be 0..1024" >&2; exit 2 ;; esac
[ "$rate" -le 60000 ] && [ "$burst" -le 1024 ] || { echo "Rate or burst exceeds the supported bound" >&2; exit 2; }

[ -x "$api" ] || { echo "Missing Go API; run ./scripts/setup.sh" >&2; exit 1; }
[ -x "$backend" ] || { echo "Missing native backend; run ./scripts/setup.sh" >&2; exit 1; }
[ -f "$studio/index.html" ] || { echo "Missing Studio production bundle; run ./scripts/setup.sh" >&2; exit 1; }
command -v curl >/dev/null 2>&1 || { echo "curl is required; run ./scripts/doctor.sh" >&2; exit 1; }

token_file="$state/auth.token"
if [ -L "$token_file" ]; then
    echo "Refusing symlinked token file: $token_file" >&2
    exit 1
fi
if [ ! -f "$token_file" ]; then
    temporary=$(mktemp "$state/auth.token.XXXXXX")
    python3.12 -c 'import secrets; print(secrets.token_urlsafe(32))' > "$temporary"
    chmod 600 "$temporary"
    mv "$temporary" "$token_file"
fi
token=$(cat "$token_file")
[ "${#token}" -ge 32 ] && [ "${#token}" -le 256 ] || { echo "Stored auth token is invalid" >&2; exit 1; }
chmod 600 "$token_file"

for item in "model.path:$model" "model.id:$model_id" "batching:$batching" "listen:$listen"; do
    name=${item%%:*}
    value=${item#*:}
    temporary=$(mktemp "$state/$name.XXXXXX")
    printf '%s\n' "$value" > "$temporary"
    mv "$temporary" "$state/$name"
done

log="$state/service.log"
[ ! -L "$log" ] || { echo "Refusing symlinked log file: $log" >&2; exit 1; }
: > "$log"

set -- "$api" \
    --listen "$listen" \
    --backend-exec "$backend" \
    --backend-state-dir "$state/backend" \
    --model "$model" \
    --model-id "$model_id" \
    --backend-batching "$batching" \
    --studio-dir "$studio" \
    --model-config-dir "$state" \
    --auth-token-env INFERNO_AUTH_TOKEN \
    --rate-per-minute "$rate" \
    --rate-burst "$burst"
INFERNO_AUTH_TOKEN=$token nohup "$@" > "$log" 2>&1 &
pid=$!
temporary=$(mktemp "$state/service.pid.XXXXXX")
printf '%s\n' "$pid" > "$temporary"
mv "$temporary" "$state/service.pid"

ready=false
cleanup=true
cleanup_start() {
    if [ "$cleanup" = true ]; then
        cleanup=false
        kill -TERM "$pid" 2>/dev/null || true
        wait "$pid" 2>/dev/null || true
        rm -f "$state/service.pid"
    fi
}
trap cleanup_start 0 HUP INT TERM
attempt=0
while [ "$attempt" -lt 240 ]; do
    if ! kill -0 "$pid" 2>/dev/null; then
        echo "PilotLlama exited during startup. Recent log:" >&2
        tail -40 "$log" >&2 || true
        exit 1
    fi
    if curl --noproxy '*' --silent --show-error --fail --max-time 2 "http://$listen/readyz" >/dev/null 2>&1; then
        ready=true
        break
    fi
    attempt=$((attempt + 1))
    sleep 0.5
done
[ "$ready" = true ] || { echo "PilotLlama did not become ready within 120 seconds; see $log" >&2; exit 1; }

cleanup=false
trap - 0 HUP INT TERM
printf 'PilotLlama is ready.\n'
printf 'API:        http://%s\n' "$listen"
printf 'Studio:     http://%s/\n' "$listen"
printf 'Model:      %s (%s)\n' "$model_id" "$batching"
printf 'Token file: %s\n' "$token_file"
printf 'Log:        %s\n' "$log"
