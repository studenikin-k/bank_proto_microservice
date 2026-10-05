#!/usr/bin/env bash
# Все сценарии подряд с сохранением итогов в load-tests/results/<label>-<время>/.
#   LABEL=micro BASE_URL=http://localhost:8080 ./load-tests/run-all-tests.sh
#   LABEL=micro-nginx BASE_URL=http://localhost ./load-tests/run-all-tests.sh
#   TESTS="smoke spike" ./load-tests/run-all-tests.sh   — только выбранные
# После прогона для микросервисов выполняется сверка БД (RECONCILE=0 — пропустить,
# например для монолита).
set -uo pipefail
cd "$(dirname "$0")/.."

LABEL=${LABEL:-run}
BASE_URL=${BASE_URL:-http://localhost:8080}
TESTS=${TESTS:-"smoke load stress spike full"}
RECONCILE=${RECONCILE:-1}
OUT_DIR="$PWD/load-tests/results/${LABEL}-$(date +%Y%m%d-%H%M%S)"
mkdir -p "$OUT_DIR"

declare -A FILES=([smoke]=smoke-test [load]=load-test [stress]=stress-test [spike]=spike-test [full]=full-scenario)

echo "==================================="
echo "Bank - Load Testing Suite ($LABEL, $BASE_URL)"
echo "Результаты: $OUT_DIR"
echo "==================================="
for t in $TESTS; do
    file=${FILES[$t]:-}
    [ -z "$file" ] && { echo "неизвестный тест: $t"; continue; }
    echo
    echo ">>> $t"
    k6 run --no-usage-report -e BASE_URL="$BASE_URL" \
        --summary-export "$OUT_DIR/$t.json" \
        "load-tests/scenarios/$file.js" 2>&1 | tee "$OUT_DIR/$t.txt"
    echo "exit=${PIPESTATUS[0]}" >> "$OUT_DIR/$t.txt"
done

if [ "$RECONCILE" = "1" ]; then
    echo
    echo ">>> сверка БД (ждём, пока recovery доведёт pending-записи)"
    sleep 40
    go run ./cmd/reconcile 2>&1 | tee "$OUT_DIR/reconcile.txt"
fi
echo
echo "Готово: $OUT_DIR"
