// Spike-тест: резкий всплеск до 600 VU на 30 счетах. Много параллельных переводов
// по одним и тем же строкам — проверка поведения под конкуренцией за блокировки.
// Ожидаемые отказы при перегрузке — 409 CONCURRENCY_CONFLICT (не дождались блокировки
// за DB_LOCK_TIMEOUT): их доля видна в transfer_rejected{code:CONCURRENCY_CONFLICT}.
import { check, sleep } from 'k6';
import { summaryTrendStats, randomAmount } from '../config.js';
import { createUsers, getAccounts, health, pickPair, transfer } from '../lib/bank.js';

const USERS = Number(__ENV.USERS || 30);

export const options = {
    stages: [
        { duration: '10s', target: 10 },
        { duration: '30s', target: 200 },
        { duration: '1m', target: 600 },
        { duration: '10s', target: 10 },
        { duration: '30s', target: 10 },
        { duration: '10s', target: 0 },
    ],
    thresholds: {
        http_req_duration: ['p(99)<3000'],
        http_req_failed: ['rate<0.01'],
        transfer_committed: ['rate>0.90'],
        checks: ['rate>0.95'],
    },
    setupTimeout: '5m',
    summaryTrendStats,
};

export function setup() {
    return { users: createUsers(USERS, 'spike') };
}

export default function (data) {
    const [from, to] = pickPair(data.users);
    check(health(), { 'health ok': (r) => r.status === 200 });
    check(getAccounts(from), { 'accounts retrieved': (r) => r.status === 200 });
    check(transfer(from, to, randomAmount(5, 25)), { 'transfer committed': (r) => r.status === 201 });
    sleep(0.3);
}
