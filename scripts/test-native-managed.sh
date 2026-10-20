#!/bin/sh
# Explicit existing weights only; no build or dependency/model downloads.
set -eu
root=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
if [ "$#" -ne 1 ] || [ ! -f "$1" ]; then
    echo "Usage: sh scripts/test-native-managed.sh EXISTING_LOCAL_GGUF" >&2
    exit 2
fi
model_dir=$(CDPATH= cd -- "$(dirname -- "$1")" && pwd)
export INFERNO_NATIVE_RPC_MODEL="$model_dir/$(basename -- "$1")"
export INFERNO_NATIVE_RPC_SERVER="$root/build/grpc/inferno_rpc_server"
exec sh "$root/scripts/go-local.sh" test -race -count=1 -timeout=90s ./internal/backendprocess -run '^TestNativeManagedLifecycle$' -v
