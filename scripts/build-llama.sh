#!/bin/sh
set -eu
root=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
cd "$root"
expected=$(cat third_party/llama.cpp-revision.txt)
actual=$(git -C third_party/llama.cpp rev-parse HEAD)
if [ "$actual" != "$expected" ]; then
    echo "llama.cpp revision differs from the project pin." >&2
    exit 1
fi
if [ -n "$(git -C third_party/llama.cpp status --porcelain)" ]; then
    echo "llama.cpp checkout has local changes; review them before building." >&2
    exit 1
fi
cmake -S third_party/llama.cpp -B build/llama-metal -G Ninja \
    -DCMAKE_BUILD_TYPE=Release \
    -DGGML_METAL=ON -DGGML_METAL_EMBED_LIBRARY=ON \
    -DGGML_OPENMP=OFF -DGGML_CPU_KLEIDIAI=OFF -DGGML_CCACHE=OFF \
    -DLLAMA_OPENSSL=OFF -DLLAMA_BUILD_BORINGSSL=OFF -DLLAMA_BUILD_LIBRESSL=OFF \
    -DLLAMA_BUILD_COMMON=ON \
    -DLLAMA_BUILD_SERVER=OFF -DLLAMA_BUILD_APP=OFF \
    -DLLAMA_BUILD_EXAMPLES=OFF -DLLAMA_BUILD_TESTS=OFF \
    -DLLAMA_BUILD_TOOLS=ON -DLLAMA_TOOLS_INSTALL=OFF -DLLAMA_TESTS_INSTALL=OFF
cmake --build build/llama-metal --target llama-completion llama-bench llama-common -j 4
