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
draft_model=${PILOTLLAMA_DRAFT_MODEL:-}
model_selected=false
[ -n "$model" ] && model_selected=true
draft_selection=auto
[ -n "$draft_model" ] && draft_selection=explicit
context_tokens=${PILOTLLAMA_CONTEXT_TOKENS:-}
max_output_tokens=${PILOTLLAMA_MAX_OUTPUT_TOKENS:-}
listen=${PILOTLLAMA_LISTEN:-127.0.0.1:8080}
rate=${PILOTLLAMA_RATE_PER_MINUTE:-60}
burst=${PILOTLLAMA_RATE_BURST:-4}

usage() {
    echo "Usage: ./scripts/start.sh [--model ABSOLUTE_GGUF] [--model-id ID]" >&2
    echo "                          [--batching queue|continuous] [--listen 127.0.0.1:PORT]" >&2
    echo "                          [--context-tokens N] [--max-output-tokens N]" >&2
    echo "                          [--draft-model ABSOLUTE_DFLASH_GGUF | --no-draft-model]" >&2
}

while [ "$#" -gt 0 ]; do
    if [ "$1" = "--no-draft-model" ]; then
        draft_model=
        draft_selection=disabled
        shift
        continue
    fi
    [ "$#" -ge 2 ] || { usage; exit 2; }
    case "$1" in
        --model) model=$2; model_selected=true ;;
        --model-id) model_id=$2 ;;
        --batching) batching=$2 ;;
        --draft-model) draft_model=$2; draft_selection=explicit ;;
        --listen) listen=$2 ;;
        --context-tokens) context_tokens=$2 ;;
        --max-output-tokens) max_output_tokens=$2 ;;
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

saved_model=
if [ -f "$state/model.path" ]; then saved_model=$(cat "$state/model.path"); fi
if [ -z "$model" ]; then model=$saved_model; fi
if [ -z "$model_id" ] && [ -f "$state/model.id" ]; then model_id=$(cat "$state/model.id"); fi
if [ -z "$batching" ] && [ -f "$state/batching" ]; then batching=$(cat "$state/batching"); fi
if [ -z "$context_tokens" ] && [ -f "$state/context.tokens" ]; then context_tokens=$(cat "$state/context.tokens"); fi
if [ "$draft_selection" = auto ] &&
   { [ "$model_selected" = false ] || [ -z "$saved_model" ] || [ "$model" = "$saved_model" ]; } &&
   [ -f "$state/draft-model.path" ]; then
    draft_model=$(cat "$state/draft-model.path")
fi
if [ -z "$max_output_tokens" ] && [ -f "$state/max-output.tokens" ]; then max_output_tokens=$(cat "$state/max-output.tokens"); fi
[ -n "$model_id" ] || model_id=local-model
[ -n "$batching" ] || batching=queue
[ -n "$context_tokens" ] || context_tokens=4096
[ -n "$max_output_tokens" ] || max_output_tokens=512

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
if [ -n "$draft_model" ]; then
    case "$draft_model" in /*) ;; *) echo "--draft-model must be an absolute path" >&2; exit 2 ;; esac
    [ -f "$draft_model" ] && [ ! -L "$draft_model" ] || { echo "Draft model must be an existing regular non-symlink file" >&2; exit 2; }
    case "$draft_model" in *"$newline"*|*"$carriage_return"*) echo "Draft model path contains an unsupported line break" >&2; exit 2 ;; esac
fi
case "$context_tokens" in ''|*[!0-9]*) echo "Context tokens must be an integer" >&2; exit 2 ;; esac
case "$max_output_tokens" in ''|*[!0-9]*) echo "Maximum output tokens must be an integer" >&2; exit 2 ;; esac
[ "$context_tokens" -ge 32 ] && [ "$context_tokens" -le 1048576 ] || { echo "Context tokens must be 32..1048576" >&2; exit 2; }
[ "$max_output_tokens" -ge 1 ] && [ "$max_output_tokens" -le "$context_tokens" ] || { echo "Maximum output tokens must fit inside the context" >&2; exit 2; }
if [ "$batching" = continuous ]; then
    half_context=$((context_tokens / 2))
    [ "$max_output_tokens" -le "$half_context" ] || { echo "Continuous mode output must fit each half of the shared context" >&2; exit 2; }
fi
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

for item in "model.path:$model" "draft-model.path:$draft_model" "model.id:$model_id" "batching:$batching" "context.tokens:$context_tokens" "max-output.tokens:$max_output_tokens" "listen:$listen"; do
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
    --context-tokens "$context_tokens" \
    --max-output-tokens "$max_output_tokens" \
    --studio-dir "$studio" \
    --model-config-dir "$state" \
    --auth-token-env INFERNO_AUTH_TOKEN \
    --rate-per-minute "$rate" \
    --rate-burst "$burst"
if [ -n "$draft_model" ]; then
    set -- "$@" --draft-model "$draft_model"
fi
qualification="$state/continuous-qualified"
if [ "$batching" = continuous ] && [ -f "$qualification" ] && [ ! -L "$qualification" ]; then
    qualified_path=$(sed -n '1p' "$qualification")
    qualified_fingerprint=$(sed -n '2p' "$qualification")
    current_fingerprint=$(stat -f '%z:%m' "$model")
    qualified_draft_path=$(sed -n '3p' "$qualification")
    qualified_draft_fingerprint=$(sed -n '4p' "$qualification")
    current_draft_fingerprint=
    if [ -n "$draft_model" ]; then current_draft_fingerprint=$(stat -f '%z:%m' "$draft_model"); fi
    if [ "$qualified_path" = "$model" ] && [ "$qualified_fingerprint" = "$current_fingerprint" ] &&
       { [ -z "$draft_model" ] || { [ "$qualified_draft_path" = "$draft_model" ] && [ "$qualified_draft_fingerprint" = "$current_draft_fingerprint" ]; }; }; then
        set -- "$@" --backend-qualify-continuous
    fi
fi
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
printf 'Budgets:    %s context / %s maximum output tokens\n' "$context_tokens" "$max_output_tokens"
printf 'Token file: %s\n' "$token_file"
printf 'Log:        %s\n' "$log"
