#!/usr/bin/env bash
# Поиск точки насыщения: ступени постоянной частоты запросов (открытая модель).
#
#   WORKLOAD=transfer LABEL=micro-1gw ./load-tests/run-capacity.sh
#   WORKLOAD=mixed LABEL=micro-nginx-4gw BASE_URL=http://localhost ./load-tests/run-capacity.sh
#
# Для каждой ступени сохраняются: итог k6 (CSV/JSON), загрузка CPU контейнеров
# (docker stats), гистограммы латентности сервисов (/debug/vars) и счётчики PostgreSQL.
# По загрузке CPU на ступени, где система перестала справляться, видно узкое место.
set -uo pipefail
cd "$(dirname "$0")/.."

WORKLOAD=${WORKLOAD:-mixed}
RATES=${RATES:-"1500 2500 3000 3500 4000 "}
DURATION=${DURATION:-60s}
WARMUP=${WARMUP:-20s}
BASE_URL=${BASE_URL:-http://localhost:8080}
LABEL=${LABEL:-run}
SLO_P99_MS=${SLO_P99_MS:-500}
USERS=${USERS:-}
STOP_AFTER=${STOP_AFTER:-2} # остановиться после стольких ступеней SATURATED подряд

if ! curl -sf -o /dev/null --max-time 3 "$BASE_URL/health"; then
    echo "стенд не отвечает на $BASE_URL/health — сначала make up (или запустите монолит)"
    exit 1
fi

OUT_DIR="$PWD/load-tests/results/capacity/${LABEL}-${WORKLOAD}-$(date +%Y%m%d-%H%M%S)"
mkdir -p "$OUT_DIR"

{
    echo "label=$LABEL workload=$WORKLOAD base_url=$BASE_URL rates=[$RATES] duration=$DURATION warmup=$WARMUP slo_p99_ms=$SLO_P99_MS"
    echo "date=$(date -Iseconds) host_cpus=$(nproc)"
    docker compose ps --format '{{.Service}} {{.Status}}' 2>/dev/null
    docker inspect --format '{{.Name}} cpus={{.HostConfig.NanoCpus}}' $(docker compose ps -q 2>/dev/null) 2>/dev/null
} > "$OUT_DIR/config.txt"

go_services() { docker compose ps --format '{{.Name}}' 2>/dev/null | grep -E 'auth|account|transaction|gateway' | grep -v postgres; }

reset_metrics() {
    for c in $(go_services); do
        docker exec "$c" wget -q -O /dev/null --post-data='' http://127.0.0.1:6060/debug/metrics/reset 2>/dev/null
    done
}

snapshot_metrics() {
    for c in $(go_services); do
        echo "== $c"
        docker exec "$c" wget -q -O - http://127.0.0.1:6060/debug/vars 2>/dev/null
        echo
    done > "$1"
}

pg_stats() {
    for db in postgres_accounts:account_user:account_db postgres_transactions:tx_user:tx_db; do
        IFS=: read -r svc user name <<< "$db"
        echo "$svc $(docker compose exec -T "$svc" psql -U "$user" -d "$name" -At -c \
            "SELECT 'commits=' || xact_commit || ' rollbacks=' || xact_rollback || ' deadlocks=' || deadlocks FROM pg_stat_database WHERE datname = current_database()" 2>/dev/null)"
    done
}

sample_cpu() { # фоновый сбор docker stats до остановки
    while true; do
        docker stats --no-stream --format '{{.Name}},{{.CPUPerc}},{{.MemUsage}}' 2>/dev/null | sed "s/^/$(date +%s),/"
    done > "$1"
}

cpu_top() { # средняя загрузка CPU каждого контейнера за ступень, по убыванию
    awk -F, '{gsub(/%/, "", $3); sum[$2] += $3; n[$2]++} END {for (c in sum) printf "%-32s %6.1f%%\n", c, sum[c] / n[c]}' "$1" | sort -k2 -nr
}

echo "Результаты: $OUT_DIR"
saturated=0
for rate in $RATES; do
    step=$(printf '%05d' "$rate")
    users_args=()
    [ -f "$OUT_DIR/users.json" ] && users_args=(-e "USERS_FILE=$OUT_DIR/users.json")
    [ -n "$USERS" ] && users_args+=(-e "USERS=$USERS")

    reset_metrics
    pg_stats > "$OUT_DIR/pg-$step-before.txt"
    sample_cpu "$OUT_DIR/cpu-$step.csv" &
    sampler=$!

    k6 run --quiet --no-usage-report \
        -e BASE_URL="$BASE_URL" -e WORKLOAD="$WORKLOAD" -e RATE="$rate" \
        -e DURATION="$DURATION" -e WARMUP="$WARMUP" -e SLO_P99_MS="$SLO_P99_MS" \
        -e LABEL="$LABEL" -e OUT_DIR="$OUT_DIR" "${users_args[@]}" \
        load-tests/scenarios/capacity.js 2> "$OUT_DIR/k6-$step.log"

    kill "$sampler" 2>/dev/null; wait "$sampler" 2>/dev/null
    snapshot_metrics "$OUT_DIR/services-$step.txt"
    pg_stats > "$OUT_DIR/pg-$step-after.txt"
    echo "  CPU контейнеров (среднее за ступень):"
    cpu_top "$OUT_DIR/cpu-$step.csv" | head -5 | sed 's/^/    /' | tee "$OUT_DIR/cpu-$step-top.txt"

    if [ ! -f "$OUT_DIR/step-$step.csv" ]; then
        echo "  ступень $rate не дала результата, см. $OUT_DIR/k6-$step.log"
        break
    fi
    if tail -n 1 "$OUT_DIR/step-$step.csv" | grep -q ',SATURATED$'; then
        saturated=$((saturated + 1))
        [ "$saturated" -ge "$STOP_AFTER" ] && { echo "  $STOP_AFTER ступени подряд SATURATED — предел найден"; break; }
    else
        saturated=0
    fi
done

summary="$OUT_DIR/summary.csv"
first=1
for f in "$OUT_DIR"/step-*.csv; do
    [ -f "$f" ] || continue
    if [ $first -eq 1 ]; then cat "$f"; first=0; else tail -n +2 "$f"; fi
done > "$summary"

echo
column -s, -t < "$summary"
echo
echo "Сводка: $summary"
echo "Проверка согласованности БД после теста: make reconcile"
