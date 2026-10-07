#!/usr/bin/env python3
"""Comparison result collector. Python standard library; no bank HTTP requests."""
import argparse
import csv
import json
import math
import os
from pathlib import Path
import subprocess
import time

PROJECT = "bank-course-comparison"
def compose(profile, *args, check=True):
    return subprocess.run(["docker", "compose", "-p", PROJECT, "-f", str(profile), *args],
        text=True, capture_output=True, check=check, timeout=120)
def sql(profile, service, user, db, query):
    result = compose(profile, "exec", "-T", service, "psql", "-X", "-qAt",
        "-v", "ON_ERROR_STOP=1", "-U", user, "-d", db, "-c", query)
    return json.loads(result.stdout)

MONO_LEDGER = """
WITH movements AS (
 SELECT from_account_id id, -total_debit delta FROM transactions WHERE status='completed'
 UNION ALL SELECT to_account_id, amount FROM transactions WHERE status='completed'
 UNION ALL SELECT fee_account_id, fee_amount FROM transactions WHERE status='completed'
), expected AS (
 SELECT a.id, a.balance, CASE WHEN a.id='00000000000001' THEN 0 ELSE 20000 END
 + COALESCE(SUM(m.delta),0) AS expected FROM accounts a LEFT JOIN movements m ON a.id=m.id
 GROUP BY a.id, a.balance
)
SELECT json_build_object(
 'accounts', (SELECT count(*) FROM accounts),
 'completed', (SELECT count(*) FROM transactions WHERE status='completed'),
 'pending', (SELECT count(*) FROM transactions WHERE status='pending'),
 'bad_balances', (SELECT count(*) FROM expected WHERE balance<>expected),
 'negative_balances', (SELECT count(*) FROM accounts WHERE balance<0),
 'money_difference_cents', (SELECT (count(*)-1)*2000000-SUM(balance)*100 FROM accounts),
 'bad_transaction_amounts', (SELECT count(*) FROM transactions WHERE
    amount<=0 OR total_debit<>amount+fee_amount OR
    fee_amount<>round(amount*fee_percent/100,2) OR status<>'completed')
)
"""
MICRO_LEDGER = """
WITH movements AS (
 SELECT from_account_id id, -(amount+fee) delta FROM applied_transfers WHERE status='applied'
 UNION ALL SELECT to_account_id, amount FROM applied_transfers WHERE status='applied'
 UNION ALL SELECT '00000000-0000-0000-0000-000000000001', fee FROM applied_transfers
 WHERE status='applied' AND fee_swept
), expected AS (
 SELECT a.id, a.balance, a.opening_balance+COALESCE(SUM(m.delta),0) AS expected
 FROM accounts a LEFT JOIN movements m ON a.id=m.id GROUP BY a.id,a.balance,a.opening_balance
)
SELECT json_build_object(
 'accounts', (SELECT count(*) FROM accounts),
 'applied', (SELECT count(*) FROM applied_transfers WHERE status='applied'),
 'bad_balances', (SELECT count(*) FROM expected WHERE balance<>expected),
 'negative_balances', (SELECT count(*) FROM accounts WHERE balance<0),
 'money_difference_cents', (SELECT SUM(opening_balance)-SUM(balance)-
    (SELECT COALESCE(SUM(fee),0) FROM applied_transfers WHERE status='applied' AND NOT fee_swept) FROM accounts)
)
"""
DB_STATS = """SELECT json_build_object('database',current_database(),
 'stats',(SELECT row_to_json(s) FROM pg_stat_database s WHERE datname=current_database()),
 'waits',(SELECT COALESCE(json_agg(w),'[]'::json) FROM
 (SELECT wait_event_type,wait_event,count(*) FROM pg_stat_activity WHERE datname=current_database()
 AND pid<>pg_backend_pid() GROUP BY wait_event_type,wait_event) w))"""
def databases(target):
    return [("db", "user", "bank")] if target == "mono" else [
        ("postgres_auth", "auth_user", "auth_db"),
        ("postgres_accounts", "account_user", "account_db"),
        ("postgres_transactions", "tx_user", "tx_db")]

def collect(args):
    dest = Path(args.directory)
    stats = {}
    for service, user, db in databases(args.target):
        stats[service] = sql(args.profile, service, user, db, DB_STATS)
    (dest / ("pg-" + args.phase + ".json")).write_text(json.dumps(stats, indent=2))
    if args.phase == "before":
        return
    report = {"ok": False}
    try:
        if args.target == "mono":
            report["ledger"] = sql(args.profile, "db", "user", "bank", MONO_LEDGER)
            report["ok"] = all(report["ledger"][key] == 0 for key in
                ["bad_balances", "negative_balances", "money_difference_cents", "bad_transaction_amounts", "pending"])
        else:
            # Read only after arrivals stop and recovery has settled.
            result = compose(args.profile, "exec", "-T", "gateway", "/app/reconcile",
                "-accounts", "postgres://account_user:account_password@postgres_accounts:5432/account_db?sslmode=disable",
                "-transactions", "postgres://tx_user:tx_password@postgres_transactions:5432/tx_db?sslmode=disable",
                "-pending-grace", "0s", check=False)
            (dest / "reconcile.txt").write_text(result.stdout + result.stderr)
            report["reconcile_exit_code"] = result.returncode
            report["ledger"] = sql(args.profile, "postgres_accounts", "account_user", "account_db", MICRO_LEDGER)
            report["transactions"] = sql(args.profile, "postgres_transactions", "tx_user", "tx_db",
                "SELECT json_build_object('completed',count(*) FILTER(WHERE status='completed'),"
                "'failed',count(*) FILTER(WHERE status='failed'),'pending',count(*) FILTER(WHERE status='pending')) FROM transactions")
            report["ok"] = result.returncode == 0 and report["transactions"]["pending"] == 0 and all(
                report["ledger"][key] == 0 for key in ["bad_balances", "negative_balances", "money_difference_cents"])
    except Exception as e:
        report["error"] = str(e)
    (dest / "reconciliation.json").write_text(json.dumps(report, indent=2))
    if not report["ok"]:
        raise SystemExit(2)

def sample(args):
    dest = Path(args.directory)
    ids = compose(args.profile, "ps", "-q").stdout.split()
    previous_ticks = None
    previous_time = None
    hz = os.sysconf("SC_CLK_TCK")
    with (dest / "telemetry.jsonl").open("w") as output:
        while True:
            now = time.monotonic()
            try:
                raw = Path("/proc/" + str(args.pid) + "/stat").read_text().rsplit(")",1)[1].split()
                ticks = int(raw[11]) + int(raw[12])
                cpu = None if previous_ticks is None else 100 * (ticks-previous_ticks)/hz/(now-previous_time)
                previous_ticks, previous_time = ticks, now
                status = Path("/proc/" + str(args.pid) + "/status").read_text().splitlines()
                rss = next((int(line.split()[1]) for line in status if line.startswith("VmRSS:")), None)
            except (FileNotFoundError, ProcessLookupError):
                return
            try:
                result = subprocess.run(["docker", "stats", "--no-stream", "--format", "{{json .}}", *ids],
                    capture_output=True, text=True, timeout=10, check=True)
                containers = [json.loads(line) for line in result.stdout.splitlines() if line]
                row = {"timestamp": time.time(), "k6_pid": args.pid, "k6_cpu_pct_one_core": cpu,
                    "k6_rss_kib": rss, "host_load": os.getloadavg(), "containers": containers}
                if args.target:
                    row["db_waits"] = {s: sql(args.profile,s,u,d,DB_STATS)["waits"] for s,u,d in databases(args.target)}
                output.write(json.dumps(row) + "\n")
                output.flush()
            except Exception as e:
                output.write(json.dumps({"timestamp":time.time(),"collector_error":str(e)}) + "\n")
                output.flush()
            time.sleep(max(0.1, 5-(time.monotonic()-now)))

def values(metrics, base, **tags):
    for key, metric in metrics.items():
        name, _, selector = key.partition("{")
        actual = dict(x.split(":",1) for x in selector.rstrip("}").split(",") if ":" in x)
        if name == base and actual == tags:
            return metric.get("values", {})
    return {}

def evaluate(summary, code, reconcile, freshness=None):
    c, m = summary.get("config", {}), summary.get("metrics", {})
    row = {"status": "INVALID", "k6_exit_code": code, **c,
        "api_read_after_write_fresh": freshness, "db_consistent": reconcile.get("ok",False),
        "reconciliation_available": bool(reconcile.get("ledger")) and not reconcile.get("error") and reconcile.get("reconcile_exit_code",0) != 2}
    if not c or not m:
        row["reason"] = "missing config or legacy k6 metrics"
        return row
    scope = {"scenario":"measure"}
    def count(name): return values(m,name,**scope).get("count",0)
    planned = c.get("planned_iterations", (c.get("rate") or 0)*c["duration_seconds"])
    scheduling_tolerance = max(2,(c.get("peak_rate") or c.get("rate") or 1)*0.02*max(1,len(c.get("phases",[]))))
    started, done, within, drops = [count(n) for n in [
        "compare_started","compare_done","compare_done_in_window","dropped_iterations"]]
    successful_within = values(m,"compare_success_done_in_window",**scope).get("count")
    lat = values(m,"compare_latency_ms",**scope)
    ok = values(m,"compare_operation_ok",**scope).get("rate")
    tx = values(m,"compare_transfer_ok",**scope).get("rate")
    last = values(m,"compare_start_offset_ms",**scope).get("max",0)
    error = values(m,"http_req_failed",**scope).get("rate")
    finish_ms = values(m,"compare_finish_offset_ms",**scope).get("max")
    ops = ["read"] if c["workload"]=="read" else ["read","transfer"] if c["workload"]=="mixed" else ["transfer"]
    required_operation_metrics = all(
        values(m,"compare_operation_ok",scenario="measure",operation=op).get("rate") is not None and
        isinstance(values(m,"compare_latency_ms",scenario="measure",operation=op).get("p(99)"),(int,float)) and
        math.isfinite(values(m,"compare_latency_ms",scenario="measure",operation=op)["p(99)"])
        for op in ops)
    if c["workload"]!="read" and tx is None: required_operation_metrics=False
    cohort_seconds = max(c["duration_seconds"],(finish_ms or 0)/1000)
    measure_epoch = values(m,"compare_phase_start_epoch_ms",scenario="measure").get("max")
    row.update(measure_start_epoch_ms=measure_epoch,
        measure_end_epoch_ms=None if measure_epoch is None else measure_epoch+c["duration_seconds"]*1000,
        last_completion_epoch_ms=None if measure_epoch is None or finish_ms is None else measure_epoch+finish_ms)
    acknowledged = values(m,"compare_transfer_committed_count").get("count",0)
    attempted = values(m,"compare_transfer_attempt_count").get("count",0)
    ledger = reconcile.get("ledger",{})
    db_completed = reconcile.get("transactions",{}).get("completed",ledger.get("completed"))
    counts_match = (db_completed is not None and acknowledged<=db_completed<=attempted and
        ledger.get("accounts")==c["users"]+1)
    row.update(transfer_attempted=attempted,transfer_acknowledged=acknowledged,
        db_completed=db_completed,db_counts_match=counts_match,cohort_elapsed_seconds=cohort_seconds)
    row.update(planned=planned, started=started, completed=done, completed_in_window=within,
        cohort_rps_is_estimate=c.get("drain_duration_basis")=="http-time-lower-bound" and done>within,
        telemetry_window_epoch_is_approximate=c.get("clock_basis")=="executor-progress",
        wall_clock_drift_ms=values(m,"compare_wall_clock_drift_ms",**scope),
        completion_rps_in_window=within/c["duration_seconds"], completed_cohort_rps=done/cohort_seconds,
        successful_in_window=successful_within, successful_rps_in_window=None if successful_within is None else successful_within/c["duration_seconds"],
        completed_per_offered_window_second=done/c["duration_seconds"],
        drained_completions=done-within, dropped=drops, operation_ok_pct=None if ok is None else ok*100,
        transfer_ok_pct=None if tx is None else tx*100, http_failed_pct=None if error is None else error*100,
        p50_ms=lat.get("med"),p95_ms=lat.get("p(95)"),p99_ms=lat.get("p(99)"),
        read_p99_ms=values(m,"compare_latency_ms",scenario="measure",operation="read").get("p(99)"),
        transfer_p99_ms=values(m,"compare_latency_ms",scenario="measure",operation="transfer").get("p(99)"))
    row["http_status_counts"] = {s:values(m,"compare_status",scenario="measure",status=s).get("count",0)
        for s in ["0","200","201","400","401","403","404","409","422","429","500","502","503","504","other"]}
    row["error_class_counts"] = {s:values(m,"compare_error_class",scenario="measure",error_class=s).get("count",0)
        for s in ["none","business_4xx","unknown_outcome","server_5xx","transport","invalid_response"]}
    warm_started = values(m,"compare_started",scenario="warmup").get("count",0)
    warm_done = values(m,"compare_done",scenario="warmup").get("count",0)
    warm_ok = values(m,"compare_operation_ok",scenario="warmup").get("rate")
    warm_drops = values(m,"dropped_iterations",scenario="warmup").get("count",0)
    warm_complete = (abs(warm_started-c.get("warmup_rate",100)*c["warmup_seconds"])<=2 and
        warm_started==warm_done and warm_ok==1 and warm_drops==0)
    if code not in [0,99]:
        row["reason"] = "k6 aborted or execution error"
    elif not warm_complete:
        row["reason"] = "warmup did not complete cleanly; initial state or in-flight work differs"
    elif abs(started+drops-planned)>scheduling_tolerance or done!=started or (drops<=planned*.005 and last<c["duration_seconds"]*1000-1500):
        row["reason"] = "incomplete arrival window or interrupted iterations"
    elif ok is None or error is None or lat.get("p(99)") is None or not math.isfinite(lat["p(99)"]) or not required_operation_metrics or finish_ms is None:
        row["reason"] = "required metrics missing"
    elif not row["reconciliation_available"]:
        row.update(status="RECONCILIATION_UNAVAILABLE",reason="DB reconciliation could not be completed")
    elif not reconcile.get("ok",False) or not counts_match:
        row.update(status="CORRECTNESS_FAILED",reason="DB invariants or acknowledgement/DB count bounds violated",db_consistent=False)
    elif drops>planned*.005:
        row.update(status="LOAD_NOT_DELIVERED",reason="VU bound or generator/SUT capacity; cause needs telemetry")
    else:
        per_operation = all(
            values(m,"compare_operation_ok",scenario="measure",operation=op).get("rate",0)>=.99 and
            values(m,"compare_latency_ms",scenario="measure",operation=op).get("p(99)",math.inf)<=c["slo_p99_ms"]
            for op in ops)
        passed = (code==0 and within>=planned*.99 and ok>=.99 and error<=.01 and
            lat["p(99)"]<=c["slo_p99_ms"] and per_operation and
            (c["workload"]=="read" or (tx is not None and tx>=.99)))
        row.update(status="PERF_PASS" if passed else "SLO_FAILED",
            reason="performance criteria met" if passed else "latency, successful operations or on-time completion criteria failed")
    return row

def finalize(args):
    dest = Path(args.directory)
    def read(name, fallback):
        try: return json.loads((dest/name).read_text())
        except (OSError,ValueError): return fallback
    meta = read("run.json",{})
    row = evaluate(read("summary.json",{}),args.code,read("reconciliation.json",{}),meta.get("api_read_after_write_fresh"))
    row.update(target=meta.get("target"),workload=meta.get("workload"),rate=meta.get("rate"),repeat=meta.get("repeat"),
        elapsed_seconds=args.elapsed, exploratory=meta.get("exploratory",False), result_directory=str(dest))
    (dest/"result.json").write_text(json.dumps(row,indent=2))
    fields = ["target","workload","rate","repeat","status","reason","k6_exit_code","elapsed_seconds",
        "started","completed","completed_in_window","completion_rps_in_window","drained_completions",
        "dropped","operation_ok_pct","transfer_ok_pct","http_failed_pct","p50_ms","p95_ms","p99_ms",
        "read_p99_ms","transfer_p99_ms","db_consistent","reconciliation_available","db_counts_match","api_read_after_write_fresh","exploratory","result_directory"]
    with (dest.parent/"summary.csv").open("a",newline="") as f:
        writer=csv.DictWriter(f,fieldnames=fields,extrasaction="ignore")
        if f.tell()==0: writer.writeheader()
        writer.writerow(row)
    print(json.dumps({k:row.get(k) for k in ["target","workload","rate","repeat","status","reason","p99_ms","api_read_after_write_fresh"]},ensure_ascii=False))
    if row["status"] in ["INVALID","CORRECTNESS_FAILED","RECONCILIATION_UNAVAILABLE"]:
        raise SystemExit(2)

if __name__ == "__main__":
    parser=argparse.ArgumentParser()
    sub=parser.add_subparsers(dest="action",required=True)
    p=sub.add_parser("collect"); p.add_argument("target",choices=["mono","micro"]); p.add_argument("profile")
    p.add_argument("directory"); p.add_argument("phase",choices=["before","after"])
    p=sub.add_parser("sample"); p.add_argument("profile"); p.add_argument("directory"); p.add_argument("pid",type=int)
    p.add_argument("--target",choices=["mono","micro"])
    p=sub.add_parser("finalize"); p.add_argument("directory"); p.add_argument("code",type=int); p.add_argument("elapsed",type=float)
    args=parser.parse_args()
    {"collect":collect,"sample":sample,"finalize":finalize}[args.action](args)
