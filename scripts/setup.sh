#!/bin/sh
# Build PilotLlama entirely inside this project from already-approved local sources.
set -eu

root=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
cd "$root"

if [ "$#" -gt 1 ] || { [ "$#" -eq 1 ] && [ "$1" != "--check" ]; }; then
    echo "Usage: ./scripts/setup.sh [--check]" >&2
    exit 2
fi

if [ "$#" -eq 1 ]; then
    sh "$root/scripts/doctor.sh"
    printf '\nCheck complete. No files were built or installed.\n'
    exit 0
fi

sh "$root/scripts/provision-dependencies.sh"
sh "$root/scripts/doctor.sh"

printf '\nCreating the isolated Python environment if needed...\n'
if [ ! -x "$root/.venv/bin/python" ]; then
    python3.12 -m venv --without-pip "$root/.venv"
fi

printf '\nBuilding pinned llama.cpp Metal libraries...\n'
sh "$root/scripts/build-llama.sh"

printf '\nBuilding the native gRPC inference server...\n'
cmake -S "$root" -B "$root/build/grpc" -G Ninja \
    -DCMAKE_BUILD_TYPE=Release -DCMAKE_POLICY_VERSION_MINIMUM=3.5 \
    -DINFERNO_WITH_GRPC=ON -DINFERNO_WITH_LLAMA=ON -DBUILD_TESTING=OFF
cmake --build "$root/build/grpc" --target inferno_rpc_server -j 4

printf '\nVerifying generated Protocol Buffer bindings...\n'
sh "$root/scripts/generate-proto.sh" --check

printf '\nBuilding the Go API with offline project caches...\n'
sh "$root/scripts/go-local.sh" build -o "$root/build/inferno-api" ./cmd/inferno-api

printf '\nBuilding the Studio production bundle...\n'
(cd "$root/studio" && npm run build)

printf '\nPilotLlama setup completed in user space.\n'
printf 'Native backend: %s\n' "$root/build/grpc/inferno_rpc_server"
printf 'Go API:         %s\n' "$root/build/inferno-api"
printf 'Studio bundle:  %s\n' "$root/studio/dist"
printf 'No model was downloaded. A local compatible GGUF is still required.\n'
