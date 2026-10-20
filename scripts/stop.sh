#!/bin/sh
# Drain and stop only the process recorded by scripts/start.sh.
set -u
umask 077

root=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
state="$root/build/pilotllama"
api="$root/build/inferno-api"
pid_file="$state/service.pid"

if [ ! -f "$pid_file" ]; then
    echo "PilotLlama is not recorded as running."
    exit 0
fi
pid=$(cat "$pid_file" 2>/dev/null || true)
case "$pid" in ''|*[!0-9]*) echo "Invalid PID state; refusing to signal any process" >&2; exit 1 ;; esac
if ! kill -0 "$pid" 2>/dev/null; then
    rm -f "$pid_file"
    echo "Removed stale PilotLlama PID state; no process was signalled."
    exit 0
fi
command_line=$(ps -p "$pid" -o command= 2>/dev/null || true)
case "$command_line" in
    "$api"|"$api "*) ;;
    *) echo "Recorded PID does not identify the owned Go API; refusing to signal it" >&2; exit 1 ;;
esac

listen=$(cat "$state/listen" 2>/dev/null || true)
token=$(cat "$state/auth.token" 2>/dev/null || true)
if [ -n "$listen" ] && [ -n "$token" ] && command -v curl >/dev/null 2>&1; then
    if curl --noproxy '*' --silent --show-error --fail --max-time 3 \
        -X POST -H "Authorization: Bearer $token" "http://$listen/internal/v1/admission/drain" >/dev/null 2>&1; then
        echo "Admission is draining."
        attempt=0
        while [ "$attempt" -lt 60 ]; do
            snapshot=$(curl --noproxy '*' --silent --show-error --fail --max-time 2 \
                -H "Authorization: Bearer $token" "http://$listen/internal/v1/admission" 2>/dev/null || true)
            if [ -n "$snapshot" ] && printf '%s' "$snapshot" | python3.12 -c \
                'import json,sys; d=json.load(sys.stdin); raise SystemExit(0 if d.get("state") == "drained" else 1)' 2>/dev/null; then
                echo "Admission drained."
                break
            fi
            attempt=$((attempt + 1))
            sleep 0.5
        done
        [ "$attempt" -lt 60 ] || echo "Drain wait reached 30 seconds; shutdown will cancel remaining work."
    else
        echo "Admission drain was unavailable; continuing with managed shutdown."
    fi
fi

if ! kill -TERM "$pid" 2>/dev/null; then
    echo "Could not signal the recorded owned process" >&2
    exit 1
fi
attempt=0
while kill -0 "$pid" 2>/dev/null && [ "$attempt" -lt 90 ]; do
    attempt=$((attempt + 1))
    sleep 0.5
done
if kill -0 "$pid" 2>/dev/null; then
    echo "Owned Go process exceeded the 45-second outer shutdown wait." >&2
    echo "It was left running to avoid orphaning an unverified native child; inspect $state/service.log." >&2
    exit 1
fi

rm -f "$pid_file"
echo "PilotLlama stopped. Its managed native backend was joined by the Go service."
