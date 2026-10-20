#!/bin/sh
# Read-only readiness checks for the supported macOS Apple Silicon build.
set -u

root=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
failures=0
warnings=0

pass() { printf 'PASS  %s\n' "$1"; }
warn() { printf 'WARN  %s\n' "$1"; warnings=$((warnings + 1)); }
fail() { printf 'FAIL  %s\n' "$1"; failures=$((failures + 1)); }

version_at_least() {
    awk -v have="$1" -v need="$2" 'BEGIN {
        split(have, h, "."); split(need, n, ".");
        for (i = 1; i <= 3; i++) {
            hv = (h[i] == "" ? 0 : h[i]) + 0;
            nv = (n[i] == "" ? 0 : n[i]) + 0;
            if (hv > nv) exit 0;
            if (hv < nv) exit 1;
        }
        exit 0;
    }'
}

require_command() {
    label=$1
    command_name=$2
    if path=$(command -v "$command_name" 2>/dev/null); then
        pass "$label: $path"
    else
        fail "$label is missing ($command_name)"
    fi
}

check_checkout() {
    label=$1
    directory=$2
    pin_file=$3
    if [ ! -d "$directory/.git" ]; then
        fail "$label source is missing; provisioning requires explicit download approval"
        return
    fi
    expected=$(cat "$pin_file" 2>/dev/null || true)
    actual=$(git -C "$directory" rev-parse HEAD 2>/dev/null || true)
    if [ -z "$expected" ] || [ "$actual" != "$expected" ]; then
        fail "$label checkout does not match its recorded revision"
    elif [ -n "$(git -C "$directory" status --porcelain 2>/dev/null)" ]; then
        fail "$label checkout has local changes"
    else
        pass "$label source matches its recorded revision"
    fi
}

printf 'PilotLlama build doctor\nProject: %s\n\n' "$root"

os=$(uname -s 2>/dev/null || true)
arch=$(uname -m 2>/dev/null || true)
[ "$os" = Darwin ] && pass "Operating system: macOS" || fail "Supported setup requires macOS"
[ "$arch" = arm64 ] && pass "Architecture: Apple Silicon (arm64)" || fail "Supported setup requires Apple Silicon (arm64)"
if command -v sw_vers >/dev/null 2>&1; then
    version=$(sw_vers -productVersion 2>/dev/null || true)
    [ -n "$version" ] && pass "macOS version: $version" || warn "macOS version is unavailable"
fi

display_info=$(system_profiler SPDisplaysDataType 2>/dev/null || true)
case "$display_info" in
    *"Metal: Supported"*) pass "Metal: supported by the detected display device" ;;
    *"Metal: Unsupported"*) fail "Metal is reported as unsupported" ;;
    *) warn "Metal support could not be read; the native smoke check is authoritative" ;;
esac

memory_bytes=$(sysctl -n hw.memsize 2>/dev/null || true)
case "$memory_bytes" in
    ''|*[!0-9]*)
        memory_line=$(system_profiler SPHardwareDataType 2>/dev/null | awk -F: '/^[[:space:]]*Memory:/ {gsub(/^[[:space:]]+/, "", $2); print $2; exit}')
        [ -n "$memory_line" ] && pass "Unified memory: $memory_line" || warn "Unified memory is unavailable"
        ;;
    *)
        memory_gib=$((memory_bytes / 1073741824))
        pass "Unified memory: ${memory_gib} GiB"
        [ "$memory_gib" -lt 8 ] && warn "Less than 8 GiB leaves little room for local inference"
        ;;
esac

disk_kib=$(df -Pk "$root" 2>/dev/null | awk 'NR == 2 {print $4}')
case "$disk_kib" in
    ''|*[!0-9]*) warn "Available project-volume disk space is unavailable" ;;
    *)
        disk_gib=$((disk_kib / 1048576))
        if [ "$disk_gib" -lt 5 ]; then
            fail "Available disk: ${disk_gib} GiB; at least 5 GiB is required for local builds"
        elif [ "$disk_gib" -lt 10 ]; then
            warn "Available disk: ${disk_gib} GiB; 10 GiB or more is recommended"
        else
            pass "Available disk: ${disk_gib} GiB"
        fi
        ;;
esac

require_command "Git" git
require_command "Apple compiler driver" clang
require_command "CMake" cmake
require_command "Ninja" ninja
require_command "Go" go
require_command "Node.js" node
require_command "npm" npm
require_command "curl" curl
require_command "Python 3.12" python3.12

if command -v cmake >/dev/null 2>&1; then
    cmake_version=$(cmake --version 2>/dev/null | awk 'NR == 1 {print $3}')
    version_at_least "$cmake_version" 3.24.0 && pass "CMake version: $cmake_version" || fail "CMake 3.24+ is required (found $cmake_version)"
fi
if command -v go >/dev/null 2>&1; then
    go_version=$(go version 2>/dev/null | awk '{sub(/^go/, "", $3); print $3}')
    version_at_least "$go_version" 1.25.0 && pass "Go version: $go_version" || fail "Go 1.25+ is required (found $go_version)"
fi
if command -v node >/dev/null 2>&1; then
    node_version=$(node --version 2>/dev/null | sed 's/^v//')
    version_at_least "$node_version" 20.19.0 && pass "Node.js version: $node_version" || fail "Node.js 20.19+ is required (found $node_version)"
fi
if command -v python3.12 >/dev/null 2>&1; then
    python_version=$(python3.12 --version 2>&1 | awk '{print $2}')
    version_at_least "$python_version" 3.12.0 && pass "Python version: $python_version" || fail "Python 3.12+ is required"
fi

if command -v git >/dev/null 2>&1; then
    check_checkout "llama.cpp" "$root/third_party/llama.cpp" "$root/third_party/llama.cpp-revision.txt"
    if [ -d "$root/third_party/googletest/.git" ]; then
        check_checkout "GoogleTest (test-only)" "$root/third_party/googletest" "$root/third_party/googletest-revision.txt"
    else
        warn "GoogleTest source is absent; runtime setup can proceed but C++ tests cannot be built"
    fi
fi

if command -v python3.12 >/dev/null 2>&1 && command -v git >/dev/null 2>&1; then
    if python3.12 - "$root" <<'PY'
import json, pathlib, subprocess, sys
root = pathlib.Path(sys.argv[1])
try:
    pins = json.loads((root / "third_party/grpc-revisions.json").read_text())
except Exception as exc:
    print(f"gRPC pin file cannot be read: {exc}", file=sys.stderr)
    raise SystemExit(1)
for source in pins["sources"]:
    path = root / source["path"]
    if not (path / ".git").exists():
        print(f"missing {source['path']}", file=sys.stderr)
        raise SystemExit(1)
    head = subprocess.run(["git", "-C", str(path), "rev-parse", "HEAD"], text=True, capture_output=True)
    dirty = subprocess.run(["git", "-C", str(path), "status", "--porcelain"], text=True, capture_output=True)
    if head.returncode or head.stdout.strip() != source["revision"] or dirty.returncode or dirty.stdout:
        print(f"unpinned or dirty {source['path']}", file=sys.stderr)
        raise SystemExit(1)
PY
    then
        pass "gRPC source tree and nested dependencies match recorded revisions"
    else
        fail "gRPC source tree is missing, dirty, or unpinned; provisioning requires explicit approval"
    fi
fi

if [ -d "$root/studio/node_modules" ]; then
    if (cd "$root/studio" && npm ls --ignore-scripts --all >/dev/null 2>&1); then
        pass "Studio dependencies match the installed dependency tree"
    else
        fail "Studio dependencies are incomplete or inconsistent with package-lock.json"
    fi
else
    fail "Studio dependencies are missing; installation requires explicit approval"
fi

for generator in protoc-gen-go protoc-gen-go-grpc; do
    if [ -x "$root/.tools/grpc/bin/$generator" ]; then
        pass "Local Protocol Buffer generator: $generator"
    else
        fail "Local $generator is missing; provisioning requires explicit approval"
    fi
done

if [ -x "$root/scripts/go-local.sh" ] || [ -f "$root/scripts/go-local.sh" ]; then
    if sh "$root/scripts/go-local.sh" list ./... >/dev/null 2>&1; then
        pass "Go modules are available in the offline project cache"
    else
        fail "Go modules are missing from the offline project cache; download requires explicit approval"
    fi
fi

[ -x "$root/build/grpc/inferno_rpc_server" ] && pass "Native RPC server is already built" || warn "Native RPC server is not built yet"
[ -x "$root/build/inferno-api" ] && pass "Go API is already built" || warn "Go API is not built yet"
[ -f "$root/studio/dist/index.html" ] && pass "Studio production bundle is already built" || warn "Studio production bundle is not built yet"
[ -x "$root/.venv/bin/python" ] && pass "Project Python environment exists" || warn "Project Python environment will be created by setup.sh"

printf '\nSummary: %s failure(s), %s warning(s)\n' "$failures" "$warnings"
if [ "$failures" -ne 0 ]; then
    printf 'Setup cannot proceed. Resolve failures without sudo or system modification.\n' >&2
    exit 1
fi
printf 'Machine and approved local dependencies are ready for setup.\n'
