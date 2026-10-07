// Capacity-тест: одна ступень ОТКРЫТОЙ модели нагрузки.
//
// k6 запускает RATE операций в секунду независимо от того, как быстро отвечает
// система (constant-arrival-rate). Если система не успевает, растут задержки и
// dropped_iterations, а достигнутый RPS отстаёт от заданного — это и есть предел.
// В закрытой модели (VU + sleep) такого не видно: там RPS задаёт сам сценарий.
//
// Обычно запускается через run-capacity.sh, который проходит ступени RATES по очереди
// и собирает таблицу. Параметры (-e):
//   WORKLOAD  read | transfer | hot | mixed | login | history (по умолчанию mixed)
//   RATE      операций в секунду на ступени
//   DURATION  длительность замера (60s), WARMUP — прогрев перед ним (20s)
//   USERS     число пользователей со счетами (200; для hot — 20: конкуренция за строки)
//   SLO_P99_MS  порог p99 для вердикта (500)
//   OUT_DIR, LABEL, USERS_FILE — задаёт run-capacity.sh
import { check } from 'k6';
import { randomAmount, summaryTrendStats } from '../config.js';
import { createUsers, getAccounts, history, login, pick, pickPair, transfer } from '../lib/bank.js';

const WORKLOAD = __ENV.WORKLOAD || 'mixed';
const RATE = Number(__ENV.RATE || 100);
const DURATION = __ENV.DURATION || '60s';
const WARMUP = __ENV.WARMUP || '20s';
const USERS = Number(__ENV.USERS || (WORKLOAD === 'hot' ? 20 : 200));
const SLO_P99_MS = Number(__ENV.SLO_P99_MS || 500);
const LABEL = __ENV.LABEL || 'run';
const OUT_DIR = __ENV.OUT_DIR || '';
const preloadedUsers = __ENV.USERS_FILE ? JSON.parse(open(__ENV.USERS_FILE)) : null;

const WORKLOADS = ['read', 'transfer', 'hot', 'mixed', 'login', 'history'];
if (!WORKLOADS.includes(WORKLOAD)) {
    throw new Error(`неизвестный WORKLOAD=${WORKLOAD}, допустимо: ${WORKLOADS.join(', ')}`);
}

const vus = {
    preAllocatedVUs: Math.max(20, Math.ceil(RATE / 4)),
    maxVUs: Math.max(100, RATE * 2),
};

export const options = {
    scenarios: {
        warmup: { executor: 'constant-arrival-rate', exec: 'run', rate: RATE, timeUnit: '1s', duration: WARMUP, ...vus, tags: { phase: 'warmup' } },
        measure: { executor: 'constant-arrival-rate', exec: 'run', rate: RATE, timeUnit: '1s', duration: DURATION, startTime: WARMUP, gracefulStop: '10s', ...vus, tags: { phase: 'measure' } },
    },
    // Пороги объявляют подметрики фазы замера — без них их не будет в итогах.
    thresholds: {
        'http_req_duration{phase:measure}': [`p(99)<${SLO_P99_MS}`],
        'http_req_failed{phase:measure}': ['rate<0.01'],
        'iterations{scenario:measure}': ['count>=0'],
        'dropped_iterations{scenario:measure}': ['count>=0'],
        'transfer_committed{phase:measure}': ['rate>=0'],
        'transfer_rejected{phase:measure}': ['count>=0'],
        'transfer_unknown{phase:measure}': ['count>=0'],
    },
    setupTimeout: '10m',
    summaryTrendStats,
};

export function setup() {
    return { users: preloadedUsers || createUsers(USERS, `cap_${WORKLOAD}`) };
}

export function run(data) {
    const users = data.users;
    switch (WORKLOAD) {
        case 'read':
            check(getAccounts(pick(users)), { ok: (r) => r.status === 200 });
            break;
        case 'transfer':
        case 'hot':
            doTransfer(users);
            break;
        case 'mixed': // типичный банк: чтений больше, чем переводов
            if (Math.random() < 0.7) {
                check(getAccounts(pick(users)), { ok: (r) => r.status === 200 });
            } else {
                doTransfer(users);
            }
            break;
        case 'login':
            check(login(pick(users)), { ok: (r) => r.status === 200 });
            break;
        case 'history':
            check(history(pick(users)), { ok: (r) => r.status === 200 });
            break;
    }
}

function doTransfer(users) {
    const [from, to] = pickPair(users);
    const type = Math.random() < 0.7 ? 'transfer' : 'payment';
    check(transfer(from, to, randomAmount(1, 50), type), { ok: (r) => r.status === 201 });
}

function seconds(d) {
    const m = /^(\d+(?:\.\d+)?)(ms|s|m|h)$/.exec(d);
    if (!m) {
        return Number(d);
    }
    return Number(m[1]) * { ms: 0.001, s: 1, m: 60, h: 3600 }[m[2]];
}

function value(data, metric, stat) {
    const m = data.metrics[metric];
    return m && m.values[stat] !== undefined ? m.values[stat] : 0;
}

export function handleSummary(data) {
    const measured = seconds(DURATION);
    const offered = RATE;
    const achieved = value(data, 'iterations{scenario:measure}', 'count') / measured;
    const dropped = value(data, 'dropped_iterations{scenario:measure}', 'count');
    const dur = 'http_req_duration{phase:measure}';
    const row = {
        label: LABEL,
        workload: WORKLOAD,
        users: preloadedUsers ? preloadedUsers.length : USERS,
        offered_rps: offered,
        achieved_rps: achieved.toFixed(1),
        dropped: dropped,
        p50_ms: value(data, dur, 'med').toFixed(1),
        p95_ms: value(data, dur, 'p(95)').toFixed(1),
        p99_ms: value(data, dur, 'p(99)').toFixed(1),
        max_ms: value(data, dur, 'max').toFixed(1),
        http_failed_pct: (value(data, 'http_req_failed{phase:measure}', 'rate') * 100).toFixed(2),
        transfer_ok_pct: (value(data, 'transfer_committed{phase:measure}', 'rate') * 100).toFixed(2),
        transfer_rejected: value(data, 'transfer_rejected{phase:measure}', 'count'),
        transfer_unknown: value(data, 'transfer_unknown{phase:measure}', 'count'),
        max_vus: value(data, 'vus_max', 'max'),
    };
    // Ступень «держится», если система обслужила почти всю заданную нагрузку
    // без сбоев и уложилась в SLO по p99.
    const holds = achieved >= offered * 0.97 && dropped <= offered * measured * 0.01 &&
        Number(row.http_failed_pct) < 1 && Number(row.p99_ms) < SLO_P99_MS;
    row.verdict = holds ? 'OK' : 'SATURATED';

    const csv = Object.keys(row).join(',') + '\n' + Object.values(row).join(',') + '\n';
    const text = `\n[${LABEL}] ${WORKLOAD} @ ${offered} rps: достигнуто ${row.achieved_rps} rps, ` +
        `p50 ${row.p50_ms} / p95 ${row.p95_ms} / p99 ${row.p99_ms} мс, сбоев ${row.http_failed_pct}%, ` +
        `dropped ${dropped}, переводов успешно ${row.transfer_ok_pct}% -> ${row.verdict}\n`;

    const out = { stdout: text };
    if (OUT_DIR) {
        const step = String(offered).padStart(5, '0');
        out[`${OUT_DIR}/step-${step}.csv`] = csv;
        out[`${OUT_DIR}/step-${step}.json`] = JSON.stringify(data, null, 1);
        if (!preloadedUsers && data.setup_data) {
            out[`${OUT_DIR}/users.json`] = JSON.stringify(data.setup_data.users);
        }
    }
    return out;
}
