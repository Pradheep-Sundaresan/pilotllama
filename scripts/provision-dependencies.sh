#!/bin/sh
# Provision only PilotLlama's pinned project-local dependencies. Never downloads models.
set -eu
umask 077

root=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
mode=${1:-}
[ "$#" -le 1 ] && { [ -z "$mode" ] || [ "$mode" = "--check" ]; } || { echo "Usage: ./scripts/provision-dependencies.sh [--check]" >&2; exit 2; }

for command_name in git cmake ninja go node npm python3.12; do
    command -v "$command_name" >/dev/null 2>&1 || { echo "Missing system prerequisite: $command_name" >&2; exit 1; }
done

needs=false
checkout_ok() { [ -d "$1/.git" ] && [ "$(git -C "$1" rev-parse HEAD 2>/dev/null || true)" = "$2" ] && [ -z "$(git -C "$1" status --porcelain 2>/dev/null || true)" ]; }
llama_pin=$(cat "$root/third_party/llama.cpp-revision.txt")
gtest_pin=$(cat "$root/third_party/googletest-revision.txt")
checkout_ok "$root/third_party/llama.cpp" "$llama_pin" || needs=true
checkout_ok "$root/third_party/googletest" "$gtest_pin" || needs=true
if ! python3.12 - "$root" <<'PY'
import json, pathlib, subprocess, sys
root = pathlib.Path(sys.argv[1]); pins = json.loads((root / "third_party/grpc-revisions.json").read_text())
for source in pins["sources"]:
    path = root / source["path"]
    if not (path / ".git").exists(): raise SystemExit(1)
    head = subprocess.run(["git", "-C", str(path), "rev-parse", "HEAD"], capture_output=True, text=True)
    dirty = subprocess.run(["git", "-C", str(path), "status", "--porcelain"], capture_output=True, text=True)
    if head.returncode or head.stdout.strip() != source["revision"] or dirty.returncode or dirty.stdout: raise SystemExit(1)
PY
then needs=true; fi
[ -d "$root/studio/node_modules" ] && (cd "$root/studio" && npm ls --ignore-scripts --all >/dev/null 2>&1) || needs=true
[ -x "$root/.tools/grpc/bin/protoc-gen-go" ] && [ -x "$root/.tools/grpc/bin/protoc-gen-go-grpc" ] || needs=true
sh "$root/scripts/go-local.sh" list ./... >/dev/null 2>&1 || needs=true

if [ "$needs" = false ]; then
    echo "Pinned project dependencies are already complete; no network access needed."
    exit 0
fi
if [ "$mode" = "--check" ]; then
    echo "Pinned project dependencies are incomplete. Run ./scripts/setup.sh to review and approve downloads." >&2
    exit 1
fi

cat <<'EOF'
PilotLlama needs to download its pinned build dependencies:
  • llama.cpp and GoogleTest from github.com
  • gRPC, protobuf, Abseil, BoringSSL, c-ares, RE2 and zlib from github.com
  • locked Go modules from proxy.golang.org with sum.golang.org verification
  • locked Studio packages from the npm registry
Destination: this project under third_party/, build/, .tools/ and studio/node_modules/.
Planning estimate: 100–300 MB compressed sources and roughly 2–6 GB after caches/builds.
No model, system package or global tool will be downloaded or installed. No sudo is used.
EOF
if [ "${PILOTLLAMA_ACCEPT_DEPENDENCIES:-}" != 1 ]; then
    [ -t 0 ] || { echo "Noninteractive download requires PILOTLLAMA_ACCEPT_DEPENDENCIES=1" >&2; exit 1; }
    printf 'Download these project-local dependencies? [y/N] '
    read answer
    case "$answer" in y|Y|yes|YES) ;; *) echo "Dependency download cancelled."; exit 1 ;; esac
fi

clone_pin() {
    url=$1; revision=$2; destination=$3
    if [ -e "$destination" ] && [ ! -d "$destination/.git" ]; then echo "Refusing existing non-checkout: $destination" >&2; exit 1; fi
    if [ ! -d "$destination/.git" ]; then mkdir -p "$(dirname "$destination")"; git init -q "$destination"; git -C "$destination" remote add origin "$url"; fi
    if ! checkout_ok "$destination" "$revision"; then
        [ -z "$(git -C "$destination" status --porcelain 2>/dev/null || true)" ] || { echo "Refusing dirty checkout: $destination" >&2; exit 1; }
        git -C "$destination" fetch --depth 1 origin "$revision"
        git -C "$destination" checkout -q --detach "$revision"
    fi
}

clone_pin https://github.com/ggml-org/llama.cpp.git "$llama_pin" "$root/third_party/llama.cpp"
clone_pin https://github.com/google/googletest.git "$gtest_pin" "$root/third_party/googletest"
python3.12 - "$root" <<'PY'
import json, pathlib, subprocess, sys
root = pathlib.Path(sys.argv[1]); pins = json.loads((root / "third_party/grpc-revisions.json").read_text())
for source in pins["sources"]:
    path = root / source["path"]
    if path.exists() and not (path / ".git").exists():
        if any(path.iterdir()): raise SystemExit(f"refusing existing non-checkout: {path}")
        path.rmdir()
    if not (path / ".git").exists():
        path.parent.mkdir(parents=True, exist_ok=True); subprocess.run(["git", "init", "-q", str(path)], check=True); subprocess.run(["git", "-C", str(path), "remote", "add", "origin", source["url"]], check=True)
    head = subprocess.run(["git", "-C", str(path), "rev-parse", "HEAD"], capture_output=True, text=True).stdout.strip()
    dirty = subprocess.run(["git", "-C", str(path), "status", "--porcelain"], capture_output=True, text=True).stdout
    if dirty: raise SystemExit(f"refusing dirty checkout: {path}")
    if head != source["revision"]:
        subprocess.run(["git", "-C", str(path), "fetch", "--depth", "1", "origin", source["revision"]], check=True)
        subprocess.run(["git", "-C", str(path), "checkout", "-q", "--detach", source["revision"]], check=True)
PY

mkdir -p "$root/build/go-cache" "$root/build/go-mod" "$root/build/go-path" "$root/.tools/grpc/bin"
(cd "$root/controlplane" && GOCACHE="$root/build/go-cache" GOMODCACHE="$root/build/go-mod" GOPATH="$root/build/go-path" GOBIN="$root/.tools/grpc/bin" GOTOOLCHAIN=local go mod download)
(cd "$root/controlplane" && GOCACHE="$root/build/go-cache" GOMODCACHE="$root/build/go-mod" GOPATH="$root/build/go-path" GOBIN="$root/.tools/grpc/bin" GOTOOLCHAIN=local go install google.golang.org/protobuf/cmd/protoc-gen-go@v1.36.9)
(cd "$root/controlplane" && GOCACHE="$root/build/go-cache" GOMODCACHE="$root/build/go-mod" GOPATH="$root/build/go-path" GOBIN="$root/.tools/grpc/bin" GOTOOLCHAIN=local go install google.golang.org/grpc/cmd/protoc-gen-go-grpc@v1.5.1)
(cd "$root/studio" && npm ci --ignore-scripts --no-audit --no-fund)
echo "Pinned project dependencies provisioned locally. No model was downloaded."
