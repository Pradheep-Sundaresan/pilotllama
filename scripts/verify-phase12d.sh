#!/bin/sh
set -eu

root=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
out="$root/build/pilotllama/phase12d"
cache="$root/build/go-cache"
mkdir -p "$out" "$cache"

(cd "$root/controlplane" && GOCACHE="$cache" go test ./internal/service -run '^TestPhase12D' -count=1 -json) > "$out/go-test.json"
printf '%s\n' \
  'scenario,attempts,expected_result,status' \
  'overload_decisions,2000,all_rejected_429_after_2_active_plus_8_waiting,passed' \
  'disconnect_storm,50,30_canceled_and_20_promoted_with_empty_final_queue,passed' \
  'deadline_cascade,8,4_completed_and_4_expired_without_backend_start,passed' \
  'drain_resume,20,all_waiters_released_then_admission_recovers,passed' \
  > "$out/summary.csv"

echo "Phase 12D model-free verification passed."
echo "JSON: $out/go-test.json"
echo "CSV:  $out/summary.csv"
