#!/bin/sh
# Offline Go commands with project-local caches; never downloads a toolchain.
set -eu
root=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
export GOCACHE="$root/build/go-cache"
export GOPATH="$root/build/go-path"
export GOMODCACHE="$root/build/go-mod"
export GOBIN="$root/.tools/grpc/bin"
export GOPROXY=off
export GOSUMDB=off
export GOTOOLCHAIN=local
cd "$root/controlplane"
exec go "$@"
