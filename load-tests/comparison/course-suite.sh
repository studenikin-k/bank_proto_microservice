#!/usr/bin/env bash
# Four common tests, two repeats per implementation. Actual workloads are run by the user.
set -Eeuo pipefail
DIR="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
ROOT="$(cd -- "$DIR/../.." && pwd)"
export COMPARISON_DIR="$DIR"
export MONOLITH_DIR="${MONOLITH_DIR:-/home/basybemoog/golang_projects/bank-prototype}"
MODE="${1:-plan}"
case "$MODE" in plan|prepare|all|mono|micro) ;; *) echo "Usage: bash course-suite.sh plan|prepare|all|micro|mono"; exit 2;; esac
if [[ "$MODE" == plan ]]; then
    cat <<'PLAN'
Per implementation: 2 repeats of smoke + capacity + stress + spike.
Capacity: 500 750 1000 1500 2500 3000 3500 RPS, 60s each.
Stress: 600s, 500 -> 1500 -> 2500 -> 3500 -> 500, ramps and holds.
Spike: 180s, 500 -> 3500 for 30s -> 500, 1s transitions.
Per repeat: 20min of measured workload + 9x60s settling + setup/warmup/reset.
Estimate: 40-45min per repeat, 80-90min per implementation, about 3h total.
Image builds are done by prepare, outside this measurement budget.
All-mode order: micro-r1 -> mono-r1 -> mono-r2 -> micro-r2.
Normal services must be stopped before the measurements.
PLAN
    exit
fi
for command in docker k6 python3 curl git tar; do command -v "$command" >/dev/null || { echo "Missing: $command"; exit 2; }; done
PROJECT=bank-course-comparison
BASE_URL=http://127.0.0.1:18080
ALLOW_STALE_READS="${ALLOW_STALE_READS:-0}"
[[ "$ALLOW_STALE_READS" == 0 || "$ALLOW_STALE_READS" == 1 ]] || exit 2
VUS="${VUS:-512}"
[[ "$VUS" =~ ^[1-9][0-9]*$ ]] || exit 2
PREPARED="$ROOT/load-tests/results/course/prepared"
TARGET= PROFILE=
dc() { docker compose -p "$PROJECT" -f "$PROFILE" "$@"; }
if [[ "$MODE" == prepare ]]; then
    python3 "$DIR/provenance.py" capture "$ROOT" "$MONOLITH_DIR" "$PREPARED"
    : > "$PREPARED/build.log"
    for TARGET in micro mono; do
        PROFILE="$DIR/compose.$TARGET.yml"
        echo "Building $TARGET comparison image..."
        dc build 2>&1 | tee -a "$PREPARED/build.log"
    done
    docker pull postgres:16-alpine 2>&1 | tee -a "$PREPARED/build.log"
    docker pull redis:7.4-alpine 2>&1 | tee -a "$PREPARED/build.log"
    python3 "$DIR/provenance.py" seal "$ROOT" "$MONOLITH_DIR" "$PREPARED"
    echo "Prepared. Start all with: ALLOW_STALE_READS=1 bash load-tests/comparison/course-suite.sh all"
    exit
fi
python3 "$DIR/provenance.py" verify "$ROOT" "$MONOLITH_DIR" "$PREPARED"
# Use previously prepared images: no unpredictable compilation inside the 3-hour series.
for TARGET in micro mono; do
    [[ "$MODE" == all || "$MODE" == "$TARGET" ]] || continue
    docker image inspect "bank-compare-$TARGET:local" >/dev/null 2>&1 ||
        { echo "Missing comparison image. Run: bash load-tests/comparison/course-suite.sh prepare"; exit 2; }
done
while read -r id; do
    [[ -z "$id" ]] && continue
    label="$(docker inspect -f '{{index .Config.Labels "com.docker.compose.project"}}' "$id")"
    [[ "$label" == "$PROJECT" ]] || { echo "Stop unrelated container before comparison: $id ($label)"; exit 2; }
done < <(docker ps -q)
check_markers() {
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
}
check_markers
for variable in ${!K6_@}; do unset "$variable"; done
SUITE="$ROOT/load-tests/results/course/$MODE-$(date -u +%Y%m%dT%H%M%SZ)-$$"
mkdir -p "$SUITE"
cp "$PREPARED/manifest.json" "$SUITE/prepared-manifest.json"
cp "$PREPARED/build.log" "$SUITE/build.log"
echo "Results: $SUITE"
suite_begin="$(date +%s)"
k6_pid= sampler_pid= active_step= case_begin=0
cleanup() {
    [[ -z "$k6_pid" ]] || kill -TERM "$k6_pid" 2>/dev/null || true
    [[ -z "$sampler_pid" ]] || kill -TERM "$sampler_pid" 2>/dev/null || true
}
interrupt() {
    cleanup
    [[ -z "$k6_pid" ]] || wait "$k6_pid" 2>/dev/null || true
    if [[ -n "$active_step" ]]; then
        echo 130 > "$active_step/k6-exit-code.txt"
        python3 "$DIR/course_results.py" finalize "$active_step" 130 "$(( $(date +%s)-case_begin ))" || true
    fi
    echo "Interrupted; current case INVALID, completed earlier cases retained"
    exit 130
}
exit_cleanup() {
    local code=$?
    cleanup
    if [[ -n "$active_step" && ! -f "$active_step/result.json" ]]; then
        [[ "$code" != 0 ]] || code=125
        echo "$code" > "$active_step/k6-exit-code.txt"
        python3 "$DIR/course_results.py" finalize "$active_step" "$code" "$(( $(date +%s)-case_begin ))" || true
    fi
}
trap exit_cleanup EXIT
trap interrupt INT TERM
k6_args=(run --no-usage-report --config "$DIR/k6-config.json" --include-system-env-vars=false)
if k6 run --help | grep -q -- '--new-machine-readable-summary'; then
    k6_args+=(--new-machine-readable-summary=false)
fi
k6 version > "$SUITE/k6-version.txt"
docker compose version > "$SUITE/compose-version.txt"
docker info > "$SUITE/docker-info.txt"
uname -a > "$SUITE/host.txt"
lscpu >> "$SUITE/host.txt"
free -h >> "$SUITE/host.txt"
for repo in "$ROOT" "$MONOLITH_DIR"; do
    tag=micro; [[ "$repo" == "$MONOLITH_DIR" ]] && tag=mono
    git -C "$repo" rev-parse HEAD > "$SUITE/$tag-commit.txt"
    git -C "$repo" status --short > "$SUITE/$tag-status.txt"
    git -C "$repo" diff --binary HEAD > "$SUITE/$tag-working-tree.patch"
    (cd -- "$repo" && git ls-files --cached --others --exclude-standard -z |
        tar --null -czf "$SUITE/$tag-source.tar.gz" -T -)
done
python3 - "$DIR" "$SUITE" "$MODE" "$VUS" "$ALLOW_STALE_READS" <<'PY'
import hashlib,json,pathlib,sys
source,dest=map(pathlib.Path,sys.argv[1:3])
(dest/"suite-sha256.json").write_text(json.dumps({
 str(p.relative_to(source)):hashlib.sha256(p.read_bytes()).hexdigest()
 for p in source.rglob("*") if p.is_file() and "__pycache__" not in str(p)},indent=2))
(dest/"plan.json").write_text(json.dumps(dict(mode=sys.argv[3],capacity_rates=[500,750,1000,1500,2500,3000,3500],
 repeats=2,capacity_seconds=60,stress_seconds=600,spike_seconds=180,users=200,seed=42,
 vus=int(sys.argv[4]),warmup_seconds=20,warmup_rate=100,request_timeout_seconds=10,
 settle_seconds=60,slo_p99_ms=500,allow_stale_reads=sys.argv[5]=="1",
 target_wall_minutes=180 if sys.argv[3]=="all" else 90),indent=2))
PY
# Supplying both profiles to down removes only marked comparison volumes, including
# the previous implementation's volumes when switching targets.
clear_stack() {
    check_markers
    docker compose -p "$PROJECT" -f "$DIR/compose.mono.yml" -f "$DIR/compose.micro.yml" \
        down --volumes --remove-orphans >> "$SUITE/stack.log" 2>&1
}
fresh_stack() {
    clear_stack
    dc up -d --pull never --wait --wait-timeout 120 >> "$SUITE/stack.log" 2>&1
    for attempt in {1..90}; do
        if curl --silent --fail --max-time 2 "$BASE_URL/health" >/dev/null; then return; fi
        sleep 1
    done
    echo "HTTP readiness failed; see $SUITE/stack.log"; exit 2
}
capture_containers() {
    local ids
    ids="$(dc ps -aq)"
    [[ -n "$ids" ]] || return 2
    docker inspect $ids > "$1/containers.json"
    python3 - "$1/containers.json" "$1/images.json" <<'PY'
import json,subprocess,sys
containers=json.load(open(sys.argv[1]))
ids=sorted(set(c["Image"] for c in containers))
r=subprocess.run(["docker","image","inspect",*ids],check=True,text=True,capture_output=True)
open(sys.argv[2],"w").write(r.stdout)
PY
}
run_case() {
    local kind="$1" rate="$2" code elapsed case_name
    case_name="$kind"
    [[ "$kind" != capacity ]] || case_name="capacity-$rate"
    STEP="$BLOCK/$case_name"
    mkdir -p "$STEP"
    case_begin="$(date +%s)"
    python3 - "$STEP/run.json" "$SUITE" "$TARGET" "$repeat" "$kind" "$rate" "$FRESH" "$ALLOW_STALE_READS" <<'PY'
import json,sys,time
path,suite,target,repeat,kind,rate,fresh,allow=sys.argv[1:]
json.dump(dict(suite_directory=suite,target=target,repeat=int(repeat),test_kind=kind,
 workload="mixed",rate=int(rate) if kind=="capacity" else None,
 api_read_after_write_fresh=fresh=="true" if fresh in ["true","false"] else None,
 allow_stale_reads=allow=="1",created_utc=time.strftime("%Y-%m-%dT%H:%M:%SZ",time.gmtime())),
 open(path,"w"),indent=2)
PY
    active_step="$STEP"
    fresh_stack
    capture_containers "$STEP"
    python3 "$DIR/tools.py" collect "$TARGET" "$PROFILE" "$STEP" before
    echo "$TARGET repeat=$repeat $case_name (series elapsed: $(( ($(date +%s)-suite_begin)/60 )) min)"
    k6 "${k6_args[@]}" -e "BASE_URL=$BASE_URL" -e "WORKLOAD=mixed" -e "RATE=$rate" \
        -e "DURATION_SECONDS=60" -e "USERS=200" -e "SEED=42" -e "VUS=$VUS" \
        -e "WARMUP_SECONDS=20" -e "WARMUP_RATE=100" -e "REQUEST_TIMEOUT_SECONDS=10" \
        -e "SLO_P99_MS=500" -e "RUN_ID=$TARGET-r$repeat-$case_name" \
        -e "SUMMARY_FILE=$STEP/summary.json" "$DIR/$kind.js" > "$STEP/k6.log" 2>&1 &
    k6_pid=$!
    python3 "$DIR/tools.py" sample "$PROFILE" "$STEP" "$k6_pid" --target "$TARGET" > "$STEP/collector.log" 2>&1 &
    sampler_pid=$!
    set +e
    wait "$k6_pid"; code=$?
    set -e
    k6_pid=
    kill "$sampler_pid" 2>/dev/null || true
    wait "$sampler_pid" 2>/dev/null || true
    sampler_pid=
    echo "$code" > "$STEP/k6-exit-code.txt"
    [[ "$kind" == smoke ]] || sleep 60
    python3 "$DIR/tools.py" collect "$TARGET" "$PROFILE" "$STEP" after || true
    dc logs --no-color --tail 1000 > "$STEP/service-logs.txt" 2>&1
    elapsed=$(( $(date +%s)-case_begin ))
    python3 "$DIR/course_results.py" finalize "$STEP" "$code" "$elapsed"
    active_step=
    if [[ "$kind" == smoke ]]; then
        FRESH="$(python3 - "$STEP/result.json" <<'PY'
import json,sys
print("true" if json.load(open(sys.argv[1]))["api_read_after_write_fresh"] else "false")
PY
)"
        if [[ "$FRESH" != true && "$ALLOW_STALE_READS" != 1 ]]; then
            echo "Stale balances: set ALLOW_STALE_READS=1 only for explicitly exploratory comparisons."
            exit 2
        fi
    fi
}
run_block() {
    TARGET="$1"; repeat="$2"
    PROFILE="$DIR/compose.$TARGET.yml"
    BLOCK="$SUITE/$TARGET-r$repeat"
    mkdir -p "$BLOCK"
    dc config > "$BLOCK/compose-resolved.yml"
    FRESH=unknown
    run_case smoke 500
    for rate in 500 750 1000 1500 2500 3000 3500; do run_case capacity "$rate"; done
    run_case stress 500
    run_case spike 500
}
if [[ "$MODE" == all ]]; then
    run_block micro 1
    run_block mono 1
    run_block mono 2
    run_block micro 2
else
    run_block "$MODE" 1
    run_block "$MODE" 2
fi
python3 "$DIR/course_results.py" finish "$SUITE"
echo "Completed in $(( ($(date +%s)-suite_begin)/60 )) minutes. Results: $SUITE"
