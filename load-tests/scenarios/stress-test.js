// Stress-тест: длительная высокая нагрузка (закрытая модель: VU + паузы).
// Показывает, держит ли система latency и корректность часами, а не её предел
// (RPS задаётся числом VU и паузами — для поиска предела есть capacity.js).
import { check, sleep } from 'k6';
import { summaryTrendStats, randomAmount } from '../config.js';
import { createUsers, getAccounts, pickPair, transfer } from '../lib/bank.js';

const USERS = Number(__ENV.USERS || 60);

export const options = {
    stages: [
        { duration: '2m', target: 50 },
        { duration: '5m', target: 400 },
        { duration: '30m', target: 600 },
        { duration: '25m', target: 500 },
        { duration: '2m', target: 20 },
    ],
    thresholds: {
        http_req_duration: ['p(99)<2000'],
        http_req_failed: ['rate<0.01'],
        transfer_committed: ['rate>0.97'],
        checks: ['rate>0.99'],
    },
    setupTimeout: '5m',
    summaryTrendStats,
};

export function setup() {
    return { users: createUsers(USERS, 'stress') };
}

export default function (data) {
    const [from, to] = pickPair(data.users);
    check(getAccounts(from), { 'accounts retrieved': (r) => r.status === 200 });
    const type = Math.random() > 0.5 ? 'transfer' : 'payment';
    check(transfer(from, to, randomAmount(5, 35), type), { 'transaction committed': (r) => r.status === 201 });
    sleep(Math.random() * 1.5 + 0.5);
}
