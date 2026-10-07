import exec from 'k6/execution';
import { check } from 'k6';
import { Counter, Rate, Trend } from 'k6/metrics';
import { BASE_URL, USERS, SEED, TIMEOUT, STATS, integer, createUsers, getAccounts,
    validAccounts, transfer, validTransfer, choice, json } from './common.js';
import { phasePlan, phaseAt } from './profiles.js';
import { progressOffset, completionClock } from './timing.js';

export function createExercise(kind) {
    const RATE = integer('RATE', 500), requestedDuration = integer('DURATION_SECONDS', 60, 10);
    const phases = phasePlan(kind, RATE, requestedDuration);
    const DURATION = phases[phases.length - 1].end_seconds;
    const planned = phases.reduce((n, p) => n + p.planned_iterations, 0);
    const WARMUP = integer('WARMUP_SECONDS', 20), VUS = integer('VUS', 512), SLO = integer('SLO_P99_MS', 500);
    const WARMUP_RATE = Math.min(phases[0].start_rate, integer('WARMUP_RATE', 100));
    const WARMUP_VUS = Math.min(VUS, 128);
    const WORKLOAD = __ENV.WORKLOAD || 'mixed';
    if (!['read', 'transfer', 'mixed', 'hot'].includes(WORKLOAD)) throw new Error('unsupported WORKLOAD');
    if (WORKLOAD === 'hot' && USERS !== 20) throw new Error('hot requires USERS=20');
    const started = new Counter('compare_started'), done = new Counter('compare_done');
    const onTime = new Counter('compare_done_in_window'), opOK = new Rate('compare_operation_ok');
    const successfulOnTime = new Counter('compare_success_done_in_window');
    const committed = new Counter('compare_transfer_committed_count'), attempts = new Counter('compare_transfer_attempt_count');
    const finish = new Trend('compare_finish_offset_ms'), phaseStart = new Trend('compare_phase_start_epoch_ms');
    const txOK = new Rate('compare_transfer_ok'), latency = new Trend('compare_latency_ms', true);
    const lastStart = new Trend('compare_start_offset_ms'), status = new Counter('compare_status');
    const wallDrift = new Trend('compare_wall_clock_drift_ms');
    const errors = new Counter('compare_error_class'), httpFail = new Rate('compare_http_failed');
    const phaseOnTime = new Counter('compare_phase_done_in_window');
    const completedNow = new Counter('compare_phase_completions');
    const successfulNow = new Counter('compare_phase_success_completions');
    const STATUSES = ['0', '200', '201', '400', '401', '403', '404', '409', '422', '429', '500', '502', '503', '504', 'other'];
    const ERROR_CLASSES = ['none', 'business_4xx', 'unknown_outcome', 'server_5xx', 'transport', 'invalid_response'];
    const strict = kind === 'capacity';
    const thresholds = {
        'compare_started{scenario:warmup}': ['count>=0'],
        'compare_done{scenario:warmup}': ['count>=0'],
        'compare_operation_ok{scenario:warmup}': ['rate==1'],
        'dropped_iterations{scenario:warmup}': ['count==0'],
        'compare_started{scenario:measure}': ['count>=0'],
        'compare_done{scenario:measure}': ['count>=0'],
        'compare_done_in_window{scenario:measure}': ['count>=0'],
        'compare_success_done_in_window{scenario:measure}': ['count>=0'],
        'compare_start_offset_ms{scenario:measure}': ['max>=0'],
        'compare_wall_clock_drift_ms{scenario:measure}': ['max>=-1000000000000'],
        'compare_finish_offset_ms{scenario:measure}': ['max>=0'],
        'compare_phase_start_epoch_ms{scenario:measure}': ['max>=0'],
        'compare_phase_start_epoch_ms{scenario:warmup}': ['max>=0'],
        'compare_transfer_committed_count': ['count>=0'],
        'compare_transfer_attempt_count': ['count>=0'],
        'compare_operation_ok{scenario:measure}': [strict ? 'rate>=0.99' : 'rate>=0'],
        'compare_latency_ms{scenario:measure}': [strict ? 'p(99)<=' + SLO : 'max>=0'],
        'http_req_failed{scenario:measure}': [strict ? 'rate<=0.01' : 'rate>=0'],
        'dropped_iterations{scenario:measure}': [strict ? 'count<=' + Math.floor(planned * 0.005) : 'count>=0'],
    };
    const operations = WORKLOAD === 'read' ? ['read'] : WORKLOAD === 'mixed' ? ['read', 'transfer'] : ['transfer'];
    for (const op of operations) {
        thresholds['compare_latency_ms{scenario:measure,operation:' + op + '}'] = [strict ? 'p(99)<=' + SLO : 'max>=0'];
        thresholds['compare_operation_ok{scenario:measure,operation:' + op + '}'] = [strict ? 'rate>=0.99' : 'rate>=0'];
    }
    if (WORKLOAD !== 'read') thresholds['compare_transfer_ok{scenario:measure}'] = [strict ? 'rate>=0.99' : 'rate>=0'];
    for (const code of STATUSES) thresholds['compare_status{scenario:measure,status:' + code + '}'] = ['count>=0'];
    for (const code of ERROR_CLASSES) thresholds['compare_error_class{scenario:measure,error_class:' + code + '}'] = ['count>=0'];
    for (const phase of phases) {
        const s = 'scenario:measure,phase:' + phase.name;
        for (const name of ['compare_started', 'compare_done', 'compare_phase_done_in_window', 'compare_phase_completions', 'compare_phase_success_completions', 'compare_transfer_attempt_count'])
            thresholds[name + '{' + s + '}'] = ['count>=0'];
        for (const name of ['compare_operation_ok', 'compare_transfer_ok', 'compare_http_failed'])
            thresholds[name + '{' + s + '}'] = ['rate>=0'];
        thresholds['compare_latency_ms{' + s + '}'] = ['max>=0'];
        for (const op of operations) {
            thresholds['compare_latency_ms{' + s + ',operation:' + op + '}'] = ['max>=0'];
            thresholds['compare_operation_ok{' + s + ',operation:' + op + '}'] = ['rate>=0'];
        }
    }
    const measure = { exec: 'operation', timeUnit: '1s', startTime: (WARMUP + TIMEOUT + 2) + 's',
        preAllocatedVUs: VUS, maxVUs: VUS, gracefulStop: (TIMEOUT + 1) + 's' };
    if (strict) Object.assign(measure, { executor: 'constant-arrival-rate', rate: RATE, duration: DURATION + 's' });
    else Object.assign(measure, { executor: 'ramping-arrival-rate', startRate: phases[0].start_rate,
        stages: phases.map((p) => ({ duration: p.seconds + 's', target: p.target })) });
    const options = {
        scenarios: {
            warmup: { executor: 'constant-arrival-rate', exec: 'operation', rate: WARMUP_RATE, timeUnit: '1s',
                duration: WARMUP + 's', preAllocatedVUs: WARMUP_VUS, maxVUs: WARMUP_VUS, gracefulStop: (TIMEOUT + 1) + 's' },
            measure
        },
        thresholds, summaryTrendStats: STATS, setupTimeout: '5m',
    };
    function setup() { return createUsers(); }
    function operation(data) {
        const scenarioDuration = exec.scenario.name === 'measure' ? DURATION : WARMUP;
        const offset = progressOffset(exec.scenario.progress, scenarioDuration);
        wallDrift.add(Date.now() - exec.scenario.startTime - offset, { phase: exec.scenario.name });
        const phase = exec.scenario.name === 'measure' ? phaseAt(phases, offset / 1000) :
            { name: 'warmup', end_seconds: WARMUP };
        const i = exec.scenario.iterationInTest, selected = choice(i, WORKLOAD, data.users.length);
        const tags = { operation: selected.isTransfer ? 'transfer' : 'read', phase: phase.name };
        started.add(1, tags);
        phaseStart.add(exec.scenario.startTime, tags);
        lastStart.add(offset, tags);
        // Zero samples keep mandatory counters available even if every transfer is rejected.
        attempts.add(selected.isTransfer ? 1 : 0, tags);
        committed.add(0, tags);
        const from = data.users[selected.index], to = data.users[selected.other];
        const res = selected.isTransfer ? transfer(from, to, 1, selected.type,
            (__ENV.RUN_ID || 'manual') + '-' + exec.scenario.name + '-' + i) : getAccounts(from);
        const ok = Boolean(selected.isTransfer ? validTransfer(res, from, to, 1, selected.type) : validAccounts(res, from));
        latency.add(res.timings.duration, tags);
        opOK.add(ok, tags);
        httpFail.add(res.status !== (selected.isTransfer ? 201 : 200), tags);
        if (selected.isTransfer) { txOK.add(ok, tags); if (ok) committed.add(1, tags); }
        check(res, { 'business operation completed with valid response': () => ok }, tags);
        const body = json(res);
        const errorClass = ok ? 'none' : res.status === 0 ? 'transport' :
            body && body.code === 'OUTCOME_UNKNOWN' ? 'unknown_outcome' :
            res.status >= 400 && res.status < 500 ? 'business_4xx' : res.status >= 500 ? 'server_5xx' : 'invalid_response';
        errors.add(1, { ...tags, error_class: errorClass });
        status.add(1, { ...tags, status: STATUSES.includes(String(res.status)) ? String(res.status) : 'other' });
        done.add(1, tags);
        const completion = completionClock(exec.scenario.progress, scenarioDuration, offset, res.timings);
        const endOffset = completion.offset;
        finish.add(endOffset, tags);
        onTime.add(completion.inWindow ? 1 : 0, tags);
        successfulOnTime.add(ok && completion.inWindow ? 1 : 0, tags);
        phaseOnTime.add(endOffset <= phase.end_seconds * 1000 ? 1 : 0, tags);
        if (exec.scenario.name === 'measure') {
            completedNow.add(1, { phase: phaseAt(phases, endOffset / 1000).name });
            successfulNow.add(ok ? 1 : 0, { phase: phaseAt(phases, endOffset / 1000).name });
        }
    }
    function handleSummary(data) {
        const config = { schema: 3, clock_basis: 'executor-progress', drain_duration_basis: 'http-time-lower-bound', test_kind: kind, base_url: BASE_URL, workload: WORKLOAD,
            rate: strict ? RATE : null, peak_rate: Math.max(...phases.map((p) => p.target)),
            duration_seconds: DURATION, planned_iterations: planned, phases,
            warmup_seconds: WARMUP, warmup_rate: WARMUP_RATE, request_timeout_seconds: TIMEOUT,
            users: USERS, seed: SEED, vus_per_scenario: VUS, warmup_vus: WARMUP_VUS, slo_p99_ms: SLO,
            measure_start_seconds: WARMUP + TIMEOUT + 2 };
        return { [__ENV.SUMMARY_FILE || 'comparison-summary.json']: JSON.stringify({ config, ...data }, null, 2),
            stdout: 'Summary saved; runner produces verdict and per-phase results after DB reconciliation.\n' };
    }
    return { options, setup, operation, handleSummary };
}
