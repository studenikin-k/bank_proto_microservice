#!/usr/bin/env bash
# Эксперимент отказоустойчивости: под постоянной нагрузкой одна реплика шлюза
# выходит из строя на OUTAGE секунд. Сравниваются два режима:
#
#   MODE=nginx  — k6 -> nginx -> 4 реплики, отказывает одна из дополнительных реплик
#                 (стенд: GATEWAY_EXTRA_REPLICAS=3 docker compose up -d --wait)
#   MODE=direct — k6 -> единственный шлюз на :8080, отказывает он сам
#
#   FAULT=stop  — процесс убит (SIGKILL) и не перезапускается до конца окна отказа
#   FAULT=pause — процесс «завис» (docker pause): соединения принимаются, ответов нет
#
#   TARGET=account|transaction — вместо шлюза отказывает сервис за ним (режим MODE
#   задаёт только точку входа). Это проверка саги: переводы, оборванные на полпути,
#   остаются pending, recovery-воркер доводит их до конца, а сверка БД после теста
#   (выполняется автоматически) подтверждает, что деньги не потерялись и не задвоились.
#
# Результат — посекундная лента timeline.csv: сколько запросов было и сколько
# завершилось сбоем. По ней видно, сколько клиентов задел отказ и как быстро
# система восстановилась.
set -uo pipefail
cd "$(dirname "$0")/.."

MODE=${MODE:-nginx}
FAULT=${FAULT:-stop}
RATE=${RATE:-200}
WORKLOAD=${WORKLOAD:-mixed}
BEFORE=${BEFORE:-30}  # секунд нормальной работы до отказа
OUTAGE=${OUTAGE:-30}  # длительность отказа
AFTER=${AFTER:-30}    # секунд после восстановления

TARGET=${TARGET:-gateway}

case "$MODE" in
    nginx)
        BASE_URL=http://localhost
        victim=$(docker compose ps --format '{{.Name}}' gateway-extra 2>/dev/null | sort | head -1)
        [ "$TARGET" = gateway ] && [ -z "$victim" ] && { echo "нет дополнительных реплик: GATEWAY_EXTRA_REPLICAS=3 docker compose up -d --wait"; exit 1; } ;;
    direct)
        BASE_URL=http://localhost:8080
        victim=$(docker compose ps --format '{{.Name}}' gateway 2>/dev/null | head -1) ;;
    *) echo "MODE должен быть nginx или direct"; exit 1 ;;
esac
if [ "$TARGET" != gateway ]; then
    victim=$(docker compose ps --format '{{.Name}}' "$TARGET" 2>/dev/null | head -1)
    [ -z "$victim" ] && { echo "сервис $TARGET не запущен"; exit 1; }
fi

OUT_DIR="$PWD/load-tests/results/failover/${TARGET}-${MODE}-${FAULT}-$(date +%Y%m%d-%H%M%S)"
mkdir -p "$OUT_DIR"
total=$((BEFORE + OUTAGE + AFTER))
echo "target=$TARGET mode=$MODE fault=$FAULT victim=$victim rate=$RATE workload=$WORKLOAD before=${BEFORE}s outage=${OUTAGE}s after=${AFTER}s" | tee "$OUT_DIR/config.txt"

(
    sleep "$((BEFORE + 5))" # +5 секунд прогрева в capacity.js
    echo "$(date +%s) FAULT $FAULT $victim" >> "$OUT_DIR/events.txt"
    if [ "$FAULT" = pause ]; then docker pause "$victim" >/dev/null; else docker stop -t 0 "$victim" >/dev/null; fi
    sleep "$OUTAGE"
    if [ "$FAULT" = pause ]; then docker unpause "$victim" >/dev/null; else docker start "$victim" >/dev/null; fi
    echo "$(date +%s) RECOVER $victim" >> "$OUT_DIR/events.txt"
) &
chaos=$!

k6 run --quiet --no-usage-report --out "csv=$OUT_DIR/metrics.csv" \
    -e BASE_URL="$BASE_URL" -e WORKLOAD="$WORKLOAD" -e RATE="$RATE" -e USERS=100 \
    -e WARMUP=5s -e DURATION="${total}s" -e LABEL="failover-$TARGET-$MODE-$FAULT" -e OUT_DIR="$OUT_DIR" \
    load-tests/scenarios/capacity.js 2> "$OUT_DIR/k6.log"
wait "$chaos"

# Посекундная лента по сырым метрикам k6: запросы и сбои (http_req_failed = 1).
awk -F, 'NR == 1 { for (i = 1; i <= NF; i++) col[$i] = i; next }
    $col["metric_name"] == "http_req_failed" { t = $col["timestamp"]; req[t]++; fail[t] += $col["metric_value"]; if (!start || t < start) start = t }
    END { for (t in req) printf "%d,%d,%d\n", t - start, req[t], fail[t] }' "$OUT_DIR/metrics.csv" |
    sort -t, -k1 -n | { echo "second,requests,failed"; cat; } > "$OUT_DIR/timeline.csv"
rm -f "$OUT_DIR/metrics.csv" # сырые метрики занимают сотни мегабайт

failed=$(awk -F, 'NR > 1 { s += $3 } END { print s + 0 }' "$OUT_DIR/timeline.csv")
requests=$(awk -F, 'NR > 1 { s += $2 } END { print s + 0 }' "$OUT_DIR/timeline.csv")
bad_seconds=$(awk -F, 'NR > 1 && $3 > 0 { n++ } END { print n + 0 }' "$OUT_DIR/timeline.csv")
{
    echo "запросов: $requests, сбоев: $failed, секунд со сбоями: $bad_seconds"
    cat "$OUT_DIR/events.txt"
} | tee "$OUT_DIR/result.txt"
echo "Посекундная лента: $OUT_DIR/timeline.csv"

if [ "$TARGET" != gateway ]; then
    echo "Ждём recovery-воркер (RECOVERY_AGE 30s + интервал) и сверяем БД..."
    sleep 45
    go run ./cmd/reconcile 2>&1 | tee "$OUT_DIR/reconcile.txt"
fi
