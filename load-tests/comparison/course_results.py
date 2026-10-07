#!/usr/bin/env python3
"""Course suite summaries; no HTTP calls or changes to application data."""
import sys
sys.dont_write_bytecode = True
import csv
import json
from pathlib import Path
import tools

def load(path, fallback=None):
    try:
        return json.loads(Path(path).read_text())
    except (OSError, ValueError):
        return {} if fallback is None else fallback

def phase_rows(summary):
    c, metrics = summary["config"], summary["metrics"]
    rows = []
    operations = ["read"] if c["workload"]=="read" else ["read","transfer"] if c["workload"]=="mixed" else ["transfer"]
    for p in c.get("phases", []):
        scope = dict(scenario="measure", phase=p["name"])
        get = lambda metric: tools.values(metrics, metric, **scope)
        started, done = get("compare_started").get("count",0), get("compare_done").get("count",0)
        own_window = get("compare_phase_done_in_window").get("count",0)
        completions_now = get("compare_phase_completions").get("count",0)
        successes_now = get("compare_phase_success_completions").get("count")
        ok = get("compare_operation_ok").get("rate")
        tx = get("compare_transfer_ok").get("rate")
        fail = get("compare_http_failed").get("rate")
        attempted = get("compare_transfer_attempt_count").get("count",0)
        lat = get("compare_latency_ms")
        op_metrics = {op: dict(
            p99_ms=tools.values(metrics,"compare_latency_ms",**scope,operation=op).get("p(99)"),
            ok_rate=tools.values(metrics,"compare_operation_ok",**scope,operation=op).get("rate"))
            for op in operations}
        expected = p["planned_iterations"]
        # Executor drops have no dynamic phase tag. This is an estimate of starts
        # missing from the phase, not the official per-profile k6 dropped counter.
        deficit = max(0, expected-started)
        transition = p["start_rate"] != p["target"]
        if started == 0:
            status = "LOAD_NOT_DELIVERED"
        elif not lat or ok is None or fail is None or successes_now is None or (c["workload"]!="read" and attempted>0 and tx is None):
            status = "METRICS_MISSING"
        elif done != started:
            status = "INCOMPLETE_ITERATIONS"
        elif deficit > max(2, expected*.005):
            status = "LOAD_NOT_DELIVERED"
        elif c["workload"]!="read" and attempted==0:
            status = "INSUFFICIENT_SAMPLES"
        elif any(v["p99_ms"] is None or v["ok_rate"] is None for v in op_metrics.values()):
            status = "METRICS_MISSING"
        elif transition:
            status = "TRANSITION_RECORDED"
        elif (ok < .99 or fail > .01 or lat.get("p(99)",float("inf")) > c["slo_p99_ms"] or
                (c["workload"]!="read" and tx < .99) or
                any(v["p99_ms"]>c["slo_p99_ms"] or v["ok_rate"]<.99 for v in op_metrics.values())):
            status = "SLO_FAILED"
        else:
            status = "PHASE_PASS"
        # Phase latency is attributed to arrivals in the phase, including their
        # drain. A response crossing the phase boundary is not itself an error.
        rows.append({**p, "status": status, "started":started,"completed":done,
            "completed_before_phase_end":own_window,"completed_during_phase":completions_now,
            "completion_rps_during_phase":completions_now/p["seconds"],
            "successful_rps_during_phase":None if successes_now is None else successes_now/p["seconds"],
            "cohort_completions_per_phase_second":done/p["seconds"],
            "not_started_estimate":deficit,"transfer_attempts":attempted,"p50_ms":lat.get("med"),
            "p95_ms":lat.get("p(95)"),"p99_ms":lat.get("p(99)"),
            "read_p99_ms":op_metrics.get("read",{}).get("p99_ms"),
            "transfer_p99_ms":op_metrics.get("transfer",{}).get("p99_ms"),
            "read_ok_pct":None if op_metrics.get("read",{}).get("ok_rate") is None else op_metrics["read"]["ok_rate"]*100,
            "operation_ok_pct":None if ok is None else ok*100,
            "transfer_ok_pct":None if tx is None else tx*100,
            "http_failed_pct":None if fail is None else fail*100})
    return rows

def finalize(directory, code, elapsed):
    dest=Path(directory)
    meta, summary, reconcile = load(dest/"run.json"), load(dest/"summary.json"), load(dest/"reconciliation.json")
    fresh=meta.get("api_read_after_write_fresh")
    if meta["test_kind"]=="smoke":
        metric=summary.get("metrics",{})
        fresh=metric.get("read_after_write_fresh",{}).get("values",{}).get("rate")==1
        ledger=reconcile.get("ledger",{})
        completed=reconcile.get("transactions",{}).get("completed",ledger.get("completed"))
        checks=metric.get("checks",{}).get("values",{}).get("rate")
        if code not in [0,99]:
            status,reason="INVALID","smoke execution error or interruption"
        elif not reconcile.get("ledger") or reconcile.get("error") or reconcile.get("reconcile_exit_code",0)==2:
            status,reason="RECONCILIATION_UNAVAILABLE","smoke DB check unavailable"
        elif not reconcile.get("ok") or ledger.get("accounts")!=3 or completed!=2:
            status,reason="CORRECTNESS_FAILED","smoke ledger/count reconciliation failed"
        elif code!=0 or checks!=1:
            status,reason="SMOKE_FAILED","functional smoke checks failed"
        else:
            status,reason="SMOKE_PASS","functional smoke passed; freshness recorded separately"
        row=dict(status=status,reason=reason,k6_exit_code=code,db_consistent=reconcile.get("ok",False))
        row["api_read_after_write_fresh"]=fresh
    else:
        row=tools.evaluate(summary,code,reconcile,fresh)
        phases=phase_rows(summary) if summary.get("config") and summary.get("metrics") else []
        if phases:
            (dest/"phases.json").write_text(json.dumps(phases,indent=2))
            with (dest/"phases.csv").open("w",newline="") as f:
                writer=csv.DictWriter(f,fieldnames=list(phases[0]))
                writer.writeheader();writer.writerows(phases)
        if meta["test_kind"] in ["stress","spike"] and row["status"] in ["PERF_PASS","SLO_FAILED","LOAD_NOT_DELIVERED"]:
            recovery=next((p for p in phases if p["name"]=="recovery_tail"),None)
            row["recovery_tail_pass"]=bool(recovery and recovery["status"]=="PHASE_PASS")
            row["profile_phases"]=phases
            if not recovery or any(p["status"] in ["METRICS_MISSING","INCOMPLETE_ITERATIONS"] for p in phases):
                row.update(status="INVALID",reason="required temporal phase metrics missing or iterations incomplete")
            else:
                row.update(status="PROFILE_RECORDED",reason="full stress/spike profile recorded; assess each phase and recovery_tail")
    row.update(target=meta["target"],test_kind=meta["test_kind"],repeat=meta["repeat"],
        rate=meta.get("rate"),elapsed_seconds=elapsed,allow_stale_reads=meta.get("allow_stale_reads",False),
        exploratory=fresh is False,api_read_after_write_fresh=fresh,result_directory=str(dest))
    (dest/"result.json").write_text(json.dumps(row,indent=2))
    fields=["target","test_kind","repeat","rate","status","reason","k6_exit_code","elapsed_seconds",
        "started","completed","completed_in_window","completion_rps_in_window","successful_in_window","successful_rps_in_window","dropped",
        "operation_ok_pct","transfer_ok_pct","http_failed_pct","p50_ms","p95_ms","p99_ms",
        "db_consistent","db_counts_match","api_read_after_write_fresh","exploratory",
        "recovery_tail_pass","result_directory"]
    path=Path(meta["suite_directory"])/"summary.csv"
    with path.open("a",newline="") as f:
        writer=csv.DictWriter(f,fieldnames=fields,extrasaction="ignore")
        if f.tell()==0:writer.writeheader()
        writer.writerow(row)
    print(json.dumps({k:row.get(k) for k in ["target","test_kind","repeat","rate","status","p99_ms","recovery_tail_pass","api_read_after_write_fresh"]},ensure_ascii=False))
    if row["status"] in ["INVALID","SMOKE_FAILED","CORRECTNESS_FAILED","RECONCILIATION_UNAVAILABLE"]:
        raise SystemExit(2)

def finish_suite(directory):
    root=Path(directory)
    results=[load(p) for p in sorted(root.rglob("result.json"))]
    capacity=[r for r in results if r.get("test_kind")=="capacity"]
    comparison=[]
    for target in ["mono","micro"]:
        for rate in sorted({r.get("rate") for r in capacity if r.get("target")==target}):
            points=[r for r in capacity if r.get("target")==target and r.get("rate")==rate]
            repetitions={r["repeat"] for r in points}
            comparison.append(dict(target=target,rate=rate,repeats=sorted(repetitions),
                statuses=[r["status"] for r in points],
                both_repeats_performance_pass=len(points)==2 and repetitions=={1,2} and all(r["status"]=="PERF_PASS" for r in points),
                both_repeats_fresh=len(points)==2 and repetitions=={1,2} and all(r.get("api_read_after_write_fresh") is True for r in points),
                p99_ms=[r.get("p99_ms") for r in points]))
    output={"schema":1,"capacity":comparison,"complete_results":len(results),
        "planned_results_per_target":20,"note":"Performance pass with stale reads is exploratory. Stress/spike phase results are separate from capacity."}
    (root/"comparison.json").write_text(json.dumps(output,indent=2))
    print("Saved:",root/"summary.csv",root/"comparison.json")

if __name__=="__main__":
    if sys.argv[1]=="finalize":
        finalize(sys.argv[2],int(sys.argv[3]),float(sys.argv[4]))
    elif sys.argv[1]=="finish":
        finish_suite(sys.argv[2])
    else:
        raise SystemExit("Usage: course_results.py finalize DIR CODE ELAPSED | finish DIR")
