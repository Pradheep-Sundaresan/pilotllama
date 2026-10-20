#!/bin/sh
set -eu
root=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
cd "$root"
# All dependencies must already exist at their approved, pinned revisions.
cmake -S . -B build/grpc -G Ninja \
    -DCMAKE_BUILD_TYPE=Release -DCMAKE_POLICY_VERSION_MINIMUM=3.5 \
    -DINFERNO_WITH_GRPC=ON -DINFERNO_WITH_LLAMA=OFF -DBUILD_TESTING=ON
cmake --build build/grpc --target protoc grpc_cpp_plugin -j 4
cmake --build build/grpc --target inferno_proto_tests inferno_rpc_fixture -j 4
