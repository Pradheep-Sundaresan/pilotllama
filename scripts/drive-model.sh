#!/bin/sh
set -eu
root=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
python="$root/.venv/bin/python"
[ -x "$python" ] || { echo "Missing project Python; run ./scripts/setup.sh" >&2; exit 1; }
exec "$python" "$root/scripts/drive-model.py" "$@"
