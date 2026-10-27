#!/bin/sh
# Explicit existing weights only; no build, downloads, stress loop, or system changes.
set -eu
root=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
if { [ "$#" -ne 1 ] && [ "$#" -ne 2 ]; } || [ ! -f "$1" ] || { [ "$#" -eq 2 ] && [ ! -f "$2" ]; }; then
    echo "Usage: sh scripts/test-native-batching.sh EXISTING_LOCAL_GGUF [EXISTING_DFLASH_GGUF]" >&2
    exit 2
fi
model_dir=$(CDPATH= cd -- "$(dirname -- "$1")" && pwd)
export INFERNO_NATIVE_RPC_MODEL="$model_dir/$(basename -- "$1")"
export INFERNO_NATIVE_RPC_DRAFT_MODEL=
if [ "$#" -eq 2 ]; then draft_dir=$(CDPATH= cd -- "$(dirname -- "$2")" && pwd); export INFERNO_NATIVE_RPC_DRAFT_MODEL="$draft_dir/$(basename -- "$2")"; fi
export INFERNO_NATIVE_RPC_SERVER="$root/build/grpc/inferno_rpc_server"
sh "$root/scripts/go-local.sh" test -race -count=1 -timeout=60s ./internal/backendprocess -run '^TestNativeContinuousBatching$' -v
state="$root/build/pilotllama"
umask 077
mkdir -p "$state"
fingerprint=$(stat -f '%z:%m' "$INFERNO_NATIVE_RPC_MODEL")
draft_fingerprint=
if [ -n "$INFERNO_NATIVE_RPC_DRAFT_MODEL" ]; then draft_fingerprint=$(stat -f '%z:%m' "$INFERNO_NATIVE_RPC_DRAFT_MODEL"); fi
temporary=$(mktemp "$state/continuous-qualified.XXXXXX")
printf '%s\n%s\n%s\n%s\n%s\n' "$INFERNO_NATIVE_RPC_MODEL" "$fingerprint" "$INFERNO_NATIVE_RPC_DRAFT_MODEL" "$draft_fingerprint" "two-sequence+cancellation+recovery" > "$temporary"
mv "$temporary" "$state/continuous-qualified"
printf 'Qualification recorded for %s\n' "$INFERNO_NATIVE_RPC_MODEL"
