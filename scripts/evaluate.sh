#!/usr/bin/env bash
# Regenerates every number in the README. About an hour on a laptop.
#
#   scripts/evaluate.sh [seeds-per-scenario] [campaign-seeds]
#
# For each bug NebulaChaos found, run the scenario that exposed it against
# the build just before its fix and against the fixed build. Then the
# planted-bug zoo, then a broad randomized campaign on the fixed build.
set -euo pipefail
cd "$(dirname "$0")/.."
N=${1:-10}
C=${2:-12}
NC=./bin/nebulachaos
OUT=docs/results
mkdir -p "$OUT"
go build -o bin/nebulachaos ./cmd/nebulachaos

latest() { ls -td runs/campaign-*/ | head -1; }
matrix() { # name schedule before after [extra flags...]
  local name=$1 sched=$2 before=$3 after=$4; shift 4
  for ref in "$before" "$after"; do
    $NC campaign --target "$ref" --schedule "$sched" --seeds "1-$N" "$@"
    cp "$(latest)/results.md" "$OUT/$name-$ref.md"
    cp "$(latest)/results.json" "$OUT/$name-$ref.json"
  done
}

# 1. Stale reads: upstream read path vs ReadIndex.
matrix stale-reads schedules/isolate-leader.yaml nebulaos-baseline nebulaos-fixed
# 2. Silent log truncation: before vs after the recovery fix.
matrix bitrot schedules/bitrot-quorum.yaml nebulaos-fix1-readindex nebulaos-fixed --snapshot-every 1000000
# 3. Double-applied commands: before vs after exactly-once requests.
matrix duplicates schedules/crash-elections.yaml nebulaos-fix2-recovery nebulaos-fixed
#    The race is narrow; leader churn with more clients gives it more chances.
N=$((N + N / 2)) matrix churn schedules/leader-churn.yaml nebulaos-fix2-recovery nebulaos-fixed --clients 20

# The checker must catch deliberately planted bugs.
$NC zoo --target nebulaos-fixed --attempts 5
cp "$(ls -td runs/zoo-*/ | head -1)/zoo.md" "$OUT/zoo.md"

# Broad randomized campaign on the fixed build.
$NC campaign --target nebulaos-fixed --profiles partitions,pauses,crashes,disk,mixed --seeds "1-$C" --duration 20s
cp "$(latest)/results.md" "$OUT/campaign-fixed.md"
cp "$(latest)/results.json" "$OUT/campaign-fixed.json"

$NC selftest --n 20000 | tee "$OUT/selftest.txt"
