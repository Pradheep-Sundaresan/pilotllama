#!/bin/sh
set -eu
root=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
cd "$root"
mode=${1:-write}
if [ "$#" -gt 1 ] || { [ "$mode" != write ] && [ "$mode" != --check ]; }; then
    echo "Usage: sh scripts/generate-proto.sh [--check]" >&2
    exit 2
fi
protoc="$root/build/grpc/upstream-grpc/third_party/protobuf/protoc"
cpp_plugin="$root/build/grpc/upstream-grpc/grpc_cpp_plugin"
go_plugin="$root/.tools/grpc/bin/protoc-gen-go"
go_grpc_plugin="$root/.tools/grpc/bin/protoc-gen-go-grpc"
for tool in "$protoc" "$cpp_plugin" "$go_plugin" "$go_grpc_plugin"; do
    if [ ! -x "$tool" ]; then
        echo "Missing local generator: $tool; see docs/grpc-dependencies.md" >&2
        exit 1
    fi
done
[ "$("$protoc" --version)" = "libprotoc 31.1" ]
[ "$("$go_plugin" --version)" = "protoc-gen-go v1.36.9" ]
[ "$("$go_grpc_plugin" --version)" = "protoc-gen-go-grpc 1.5.1" ]
# Generate into a fresh ignored directory; compare/copy only the expected outputs.
temporary=$(mktemp -d "$root/build/proto-generation.XXXXXX")
mkdir -p "$temporary/cpp" "$temporary/go"
"$protoc" -I "$root/proto" --cpp_out="$temporary/cpp" --grpc_out="$temporary/cpp" \
    --plugin=protoc-gen-grpc="$cpp_plugin" inferno/v1/inference.proto
"$protoc" -I "$root/proto" --go_out="$temporary/go" \
    --go_opt=module=inferno.local/controlplane --go-grpc_out="$temporary/go" \
    --go-grpc_opt=module=inferno.local/controlplane \
    --plugin=protoc-gen-go="$go_plugin" --plugin=protoc-gen-go-grpc="$go_grpc_plugin" \
    inferno/v1/inference.proto
for name in inference.pb.h inference.pb.cc inference.grpc.pb.h inference.grpc.pb.cc; do
    destination="$root/build/grpc/generated/inferno/v1/$name"
    if [ "$mode" = --check ]; then
        cmp "$temporary/cpp/inferno/v1/$name" "$destination"
    elif ! cmp -s "$temporary/cpp/inferno/v1/$name" "$destination"; then
        mkdir -p "$(dirname "$destination")"
        cp "$temporary/cpp/inferno/v1/$name" "$destination"
    fi
done
for name in inference.pb.go inference_grpc.pb.go; do
    destination="$root/controlplane/gen/inferno/v1/$name"
    if [ "$mode" = --check ]; then
        cmp "$temporary/go/gen/inferno/v1/$name" "$destination"
    elif ! cmp -s "$temporary/go/gen/inferno/v1/$name" "$destination"; then
        mkdir -p "$(dirname "$destination")"
        cp "$temporary/go/gen/inferno/v1/$name" "$destination"
    fi
done
echo "Bindings generated and verified ($mode); no downloads."
