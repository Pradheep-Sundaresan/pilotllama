#!/bin/sh
set -eu
root=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
cd "$root"
INFERNO_PROTO_INTEROP_DIR=$(mktemp -d "$root/build/proto-interop.XXXXXX")
export INFERNO_PROTO_INTEROP_DIR
export INFERNO_PROTO_INTEROP_MODE=write
./build/grpc/inferno_proto_tests --gtest_filter=ProtoBindings.CrossLanguageWire
sh scripts/go-local.sh test -count=1 ./internal/prototest -run '^TestCrossLanguageWire$' -v
export INFERNO_PROTO_INTEROP_MODE=check
./build/grpc/inferno_proto_tests --gtest_filter=ProtoBindings.CrossLanguageWire
# Small binary fixtures remain under ignored build/ for inspection.
