#!/bin/sh
set -eu
root=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
cd "$root"
# Reuse the existing local gRPC build; import existing pinned llama.cpp libraries.
cmake -S . -B build/grpc -G Ninja \
    -DCMAKE_BUILD_TYPE=Release -DCMAKE_POLICY_VERSION_MINIMUM=3.5 \
    -DINFERNO_WITH_GRPC=ON -DINFERNO_WITH_LLAMA=ON -DBUILD_TESTING=ON
cmake --build build/grpc --target inferno_rpc_server -j 4
