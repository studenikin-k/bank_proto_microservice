#!/usr/bin/env bash
# Run from WSL. This runner only resets its own disposable comparison project.
set -Eeuo pipefail
DIR="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
ROOT="$(cd -- "$DIR/../.." && pwd)"
export COMPARISON_DIR="$DIR"
export MONOLITH_DIR="${MONOLITH_DIR:-/home/basybemoog/golang_projects/bank-prototype}"
TARGET="${1:-}"
WORKLOAD="${2:-mixed}"
case "$TARGET" in mono|micro) ;; *) echo "Usage: bash run.sh mono|micro smoke|read|transfer|mixed|hot"; exit 2;; esac
case "$WORKLOAD" in smoke|read|transfer|mixed|hot) ;; *) echo "Unsupported workload"; exit 2;; esac
for command in docker k6 python3 curl git tar; do command -v "$command" >/dev/null || { echo "Missing: $command"; exit 2; }; done
PROFILE="$DIR/compose.$TARGET.yml"
PROJECT=bank-course-comparison
BASE_URL=http://127.0.0.1:18080
RATES="${RATES:-250 500 750 1000 1250 1500 2000 2500 3000}"
REPEATS="${REPEATS:-3}"
DURATION_SECONDS="${DURATION_SECONDS:-60}"
WARMUP_SECONDS="${WARMUP_SECONDS:-20}"
WARMUP_RATE="${WARMUP_RATE:-100}"
REQUEST_TIMEOUT_SECONDS="${REQUEST_TIMEOUT_SECONDS:-10}"
USERS="${USERS:-200}"
if [[ "$WORKLOAD" == hot ]]; then USERS="${HOT_USERS:-20}"; fi
VUS="${VUS:-512}"
SEED="${SEED:-42}"
SLO_P99_MS="${SLO_P99_MS:-500}"
ALLOW_STALE_READS="${ALLOW_STALE_READS:-0}"
for variable in REPEATS DURATION_SECONDS WARMUP_SECONDS WARMUP_RATE REQUEST_TIMEOUT_SECONDS USERS VUS SLO_P99_MS; do
    [[ "${!variable}" =~ ^[1-9][0-9]*$ ]] || { echo "Invalid $variable"; exit 2; }
done
[[ "$SEED" =~ ^[0-9]+$ && "$USERS" -ge 2 && "$DURATION_SECONDS" -ge 10 ]] || exit 2
for rate in $RATES; do [[ "$rate" =~ ^[1-9][0-9]*$ ]] || { echo "Invalid RATE"; exit 2; }; done
[[ "$ALLOW_STALE_READS" == 0 || "$ALLOW_STALE_READS" == 1 ]] || exit 2
# Reject inherited k6 options and personal config; all test options live in this suite.
for variable in ${!K6_@}; do unset "$variable"; done
dc() { docker compose -p "$PROJECT" -f "$PROFILE" "$@"; }
# No other running containers: otherwise they compete for the same host.
while read -r id; do
    [[ -z "$id" ]] && continue
    label="$(docker inspect -f '{{index .Config.Labels "com.docker.compose.project"}}' "$id")"
    [[ "$label" == "$PROJECT" ]] || { echo "Stop unrelated containers before comparison: $id ($label)"; exit 2; }
done < <(docker ps -q)
# Refuse to reset a pre-existing project unless ALL its containers and volumes carry our marker.
while read -r id; do
    [[ -z "$id" ]] && continue
    [[ "$(docker inspect -f '{{index .Config.Labels "org.bank.course.comparison"}}' "$id")" == 1 ]] ||
        { echo "Unmarked project container; refusing reset: $id"; exit 2; }
done < <(docker ps -aq --filter "label=com.docker.compose.project=$PROJECT")
while read -r volume; do
    [[ -z "$volume" ]] && continue
    [[ "$(docker volume inspect -f '{{index .Labels "org.bank.course.comparison"}}' "$volume")" == 1 ]] ||
        { echo "Unmarked project volume; refusing reset: $volume"; exit 2; }
done < <(docker volume ls -q --filter "label=com.docker.compose.project=$PROJECT")
OUT="$ROOT/load-tests/results/comparison/$TARGET-$WORKLOAD-$(date -u +%Y%m%dT%H%M%SZ)-$$"
mkdir -p "$OUT"
k6_pid= sampler_pid= active_step= begin=0
cleanup() {
    [[ -z "$k6_pid" ]] || kill -TERM "$k6_pid" 2>/dev/null || true
    [[ -z "$sampler_pid" ]] || kill -TERM "$sampler_pid" 2>/dev/null || true
}
trap cleanup EXIT
interrupt() {
    cleanup
    [[ -z "$k6_pid" ]] || wait "$k6_pid" 2>/dev/null || true
    if [[ -n "$active_step" ]]; then
        echo 130 > "$active_step/k6-exit-code.txt"
        python3 "$DIR/tools.py" finalize "$active_step" 130 "$(( $(date +%s) - begin ))" || true
    fi
    echo "Interrupted; incomplete point is INVALID"
    exit 130
}
trap interrupt INT TERM
dc config > "$OUT/compose-resolved.yml"
dc build > "$OUT/build.log" 2>&1 || { echo "Build failed: $OUT/build.log"; exit 2; }
k6 version > "$OUT/k6-version.txt"
docker compose version > "$OUT/compose-version.txt"
uname -a > "$OUT/host.txt"
lscpu >> "$OUT/host.txt"
free -h >> "$OUT/host.txt"
docker info > "$OUT/docker-info.txt"
for repo in "$ROOT" "$MONOLITH_DIR"; do
    tag=micro; [[ "$repo" == "$MONOLITH_DIR" ]] && tag=mono
    git -C "$repo" rev-parse HEAD > "$OUT/$tag-commit.txt"
    git -C "$repo" status --short > "$OUT/$tag-status.txt"
    git -C "$repo" diff --binary HEAD > "$OUT/$tag-working-tree.patch"
    (cd -- "$repo" && git ls-files --cached --others --exclude-standard -z |
        tar --null -czf "$OUT/$tag-source.tar.gz" -T -)
done
python3 - "$DIR" "$OUT" <<'PY'
import hashlib,json,pathlib,sys
root,dest=map(pathlib.Path,sys.argv[1:])
(dest/"suite-sha256.json").write_text(json.dumps({
 str(p.relative_to(root)):hashlib.sha256(p.read_bytes()).hexdigest()
 for p in root.rglob("*") if p.is_file() and "__pycache__" not in str(p)},indent=2))
PY
fresh_stack() {
    dc down --volumes --remove-orphans >> "$OUT/stack.log" 2>&1
    dc up -d --wait >> "$OUT/stack.log" 2>&1
    for attempt in {1..90}; do
        if curl --silent --fail --max-time 2 "$BASE_URL/health" >/dev/null; then return; fi
        sleep 1
    done
    echo "HTTP readiness timeout; see $OUT/stack.log"; exit 2
}
capture_containers() {
    local ids
    ids="$(dc ps -aq)"
    [[ -n "$ids" ]] || return 2
    # IDs are returned by Docker, never shell text from user input.
    docker inspect $ids > "$1/containers.json"
    python3 - "$1/containers.json" "$1/images.json" <<'PY'
import json,subprocess,sys
containers=json.load(open(sys.argv[1]))
ids=sorted(set(c["Image"] for c in containers))
r=subprocess.run(["docker","image","inspect",*ids],check=True,capture_output=True,text=True)
open(sys.argv[2],"w").write(r.stdout)
PY
}
k6_args=(run --no-usage-report --config "$DIR/k6-config.json" --include-system-env-vars=false)
# k6 2.x can change handleSummary JSON layout; force the legacy layout when the flag is available.
if k6 run --help | grep -q -- '--new-machine-readable-summary'; then
    k6_args+=(--new-machine-readable-summary=false)
fi
fresh_stack
SMOKE="$OUT/smoke"
mkdir -p "$SMOKE"
capture_containers "$SMOKE"
set +e
k6 "${k6_args[@]}" -e "BASE_URL=$BASE_URL" -e "SUMMARY_FILE=$SMOKE/summary.json" "$DIR/smoke.js" > "$SMOKE/k6.log" 2>&1
smoke_code=$?
set -e
echo "$smoke_code" > "$SMOKE/k6-exit-code.txt"
python3 "$DIR/tools.py" collect "$TARGET" "$PROFILE" "$SMOKE" after || { echo "Smoke DB reconciliation failed"; exit 2; }
[[ "$smoke_code" == 0 ]] || { echo "Common smoke failed: $SMOKE/k6.log"; exit 2; }
FRESH="$(python3 - "$SMOKE/summary.json" <<'PY'
import json,sys
d=json.load(open(sys.argv[1]))
r=d.get("metrics",{}).get("read_after_write_fresh",{}).get("values",{}).get("rate")
print("true" if r==1 else "false")
PY
)"
if [[ "$FRESH" != true ]]; then
    echo "READ-AFTER-WRITE: stale account-list balance. Result: $SMOKE/summary.json"
    if [[ "$WORKLOAD" == mixed && "$ALLOW_STALE_READS" != 1 ]]; then
        echo "Fix cache invalidation before comparable mixed measurements."
        echo "ALLOW_STALE_READS=1 permits an explicitly marked exploratory measurement of current implementations."
        exit 2
    fi
fi
echo "Common smoke passed; API read-after-write freshness: $FRESH"
if [[ "$WORKLOAD" == smoke ]]; then echo "Results: $OUT"; exit; fi
for ((repeat=1; repeat<=REPEATS; repeat++)); do
    for rate in $RATES; do
        STEP="$OUT/r$repeat-rate$rate"
        mkdir -p "$STEP"
        fresh_stack
        capture_containers "$STEP"
        RUN_ID="$TARGET-$WORKLOAD-r$repeat-rate$rate"
        python3 - "$STEP/run.json" "$TARGET" "$WORKLOAD" "$rate" "$repeat" "$FRESH" "$ALLOW_STALE_READS" <<'PY'
import json,sys,time
path,target,workload,rate,repeat,fresh,allow=sys.argv[1:]
json.dump(dict(target=target,workload=workload,rate=int(rate),repeat=int(repeat),
 api_read_after_write_fresh=fresh=="true",allow_stale_reads=allow=="1",exploratory=workload=="mixed" and fresh!="true",created_utc=time.strftime("%Y-%m-%dT%H:%M:%SZ",time.gmtime())),open(path,"w"),indent=2)
PY
        python3 "$DIR/tools.py" collect "$TARGET" "$PROFILE" "$STEP" before
        echo "$TARGET $WORKLOAD repeat=$repeat rate=$rate ($DURATION_SECONDS seconds + setup/warmup)"
        begin="$(date +%s)"
        active_step="$STEP"
        k6 "${k6_args[@]}" -e "BASE_URL=$BASE_URL" -e "RATE=$rate" -e "WORKLOAD=$WORKLOAD" \
            -e "DURATION_SECONDS=$DURATION_SECONDS" -e "WARMUP_SECONDS=$WARMUP_SECONDS" -e "WARMUP_RATE=$WARMUP_RATE" \
            -e "REQUEST_TIMEOUT_SECONDS=$REQUEST_TIMEOUT_SECONDS" -e "USERS=$USERS" -e "VUS=$VUS" \
            -e "SEED=$SEED" -e "SLO_P99_MS=$SLO_P99_MS" -e "RUN_ID=$RUN_ID" \
            -e "SUMMARY_FILE=$STEP/summary.json" "$DIR/capacity.js" > "$STEP/k6.log" 2>&1 &
        k6_pid=$!
        python3 "$DIR/tools.py" sample "$PROFILE" "$STEP" "$k6_pid" --target "$TARGET" > "$STEP/collector.log" 2>&1 &
        sampler_pid=$!
        set +e
        wait "$k6_pid"
        code=$?
        set -e
        k6_pid=
        kill "$sampler_pid" 2>/dev/null || true
        wait "$sampler_pid" 2>/dev/null || true
        sampler_pid=
        elapsed=$(( $(date +%s) - begin ))
        echo "$code" > "$STEP/k6-exit-code.txt"
        # Wait for in-flight requests/recovery and fee sweeps; this is outside the measured interval.
        sleep 60
        python3 "$DIR/tools.py" collect "$TARGET" "$PROFILE" "$STEP" after || true
        dc logs --no-color --tail 1000 > "$STEP/service-logs.txt" 2>&1
        python3 "$DIR/tools.py" finalize "$STEP" "$code" "$elapsed"
        active_step=
        [[ "$code" == 0 || "$code" == 99 ]] || exit 2
    done
done
echo "Results: $OUT"
echo "Comparison project remains running; clean it with the documented comparison-only command."
