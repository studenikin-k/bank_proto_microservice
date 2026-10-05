// Load-тест: поведение пользователей при обычной нагрузке (закрытая модель: VU + паузы).
// Показывает latency при заданном числе пользователей. Пропускную способность
// системы он не измеряет: RPS здесь задаётся числом VU и паузами, а не сервером,
// — для поиска предела есть capacity.js.
import { check, sleep } from 'k6';
import { summaryTrendStats, randomAmount } from '../config.js';
import { createUsers, getAccounts, pickPair, transfer } from '../lib/bank.js';

const USERS = Number(__ENV.USERS || 20);

export const options = {
    stages: [
        { duration: '2m', target: 10 },
        { duration: '5m', target: 75 },
        { duration: '15m', target: 250 },
        { duration: '25m', target: 400 },
        { duration: '2m', target: 350 },
        { duration: '2m', target: 0 },
    ],
    thresholds: {
        http_req_duration: ['p(95)<800'],
        http_req_failed: ['rate<0.01'],
        transfer_committed: ['rate>0.97'],
        checks: ['rate>0.99'],
    },
    setupTimeout: '5m',
    summaryTrendStats,
};

export function setup() {
    return { users: createUsers(USERS, 'load') };
}

export default function (data) {
    const [from, to] = pickPair(data.users);
    check(getAccounts(from), { 'accounts retrieved': (r) => r.status === 200 });
    check(transfer(from, to, randomAmount(10, 60)), { 'transfer committed': (r) => r.status === 201 });
    sleep(Math.random() * 2 + 1);
}
