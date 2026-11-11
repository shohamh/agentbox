#!/usr/bin/env bash
# Phase-0 exit criterion: all E2E and UI tests pass three nightly runs in a row.
# Each run starts from a clean stack (volumes removed) and is judged on its own.
set -uo pipefail
cd "$(dirname "$0")/.."
RUNS="${RUNS:-3}"
mkdir -p test/results
: > test/results/nightly.txt
for i in $(seq 1 "$RUNS"); do
  echo "=== nightly run $i of $RUNS: $(date -u +%FT%TZ) ==="
  # the first run builds; later runs reuse the built images but recreate the stack
  if [ "$i" -gt 1 ]; then export SKIP_BUILD=1; fi
  if ! bash scripts/test.sh; then
    echo "run $i FAILED; the three-in-a-row criterion is not met" | tee -a test/results/nightly.txt
    cp -r test/results "test/results-failed-run$i" 2>/dev/null
    exit 1
  fi
  echo "run $i passed $(date -u +%FT%TZ)" | tee -a test/results/nightly.txt
  cp test/results/summary.txt "test/results/summary-run$i.txt" 2>/dev/null
done
echo "all $RUNS nightly runs passed in a row" | tee -a test/results/nightly.txt
