#!/bin/sh
# Existing MODEL-FREE fixture only; no builds or downloads.
set -eu
root=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
export INFERNO_RPC_FIXTURE="$root/build/grpc/inferno_rpc_fixture"
exec sh "$root/scripts/go-local.sh" test -race -count=1 -timeout=60s ./internal/backendprocess -run '^TestIntegration' -v
