// Полный сценарий: одновременно регистрируются новые клиенты, идут банковские
// операции и чтение. Пользователи для операций создаются в setup(): у каждого VU
// в k6 своя копия глобальных переменных, поэтому общий массив между сценариями
// не работает — данные между VU передаются только через setup().
import { check, sleep } from 'k6';
import { summaryTrendStats, randomAmount } from '../config.js';
import { createUsers, getAccounts, health, history, pickPair, transfer } from '../lib/bank.js';

const USERS = Number(__ENV.USERS || 50);

export const options = {
    scenarios: {
        user_registration: {
            executor: 'ramping-vus',
            exec: 'userRegistration',
            startVUs: 0,
            stages: [
                { duration: '1m', target: 3 },
                { duration: '3m', target: 5 },
                { duration: '1m', target: 0 },
            ],
        },
        banking_operations: {
            executor: 'ramping-vus',
            exec: 'bankingOperations',
            startTime: '1m',
            startVUs: 0,
            stages: [
                { duration: '2m', target: 10 },
                { duration: '5m', target: 45 },
                { duration: '2m', target: 10 },
                { duration: '2m', target: 0 },
            ],
        },
        read_heavy: {
            executor: 'constant-vus',
            exec: 'readHeavy',
            vus: 15,
            duration: '10m',
            startTime: '2m',
        },
    },
    thresholds: {
        http_req_duration: ['p(95)<1000'],
        http_req_failed: ['rate<0.01'],
        transfer_committed: ['rate>0.97'],
        checks: ['rate>0.99'],
    },
    setupTimeout: '5m',
    summaryTrendStats,
};

export function setup() {
    return { users: createUsers(USERS, 'full') };
}

export function userRegistration() {
    const [user] = createUsers(1, 'fullreg');
    check(getAccounts(user), { 'new user sees account': (r) => r.status === 200 && r.json('accounts').length === 1 });
    sleep(Math.random() * 3 + 2);
}

export function bankingOperations(data) {
    const [from, to] = pickPair(data.users);
    check(getAccounts(from), { 'accounts retrieved': (r) => r.status === 200 });
    if (Math.random() > 0.3) {
        const type = Math.random() > 0.6 ? 'transfer' : 'payment';
        check(transfer(from, to, randomAmount(10, 40), type), { 'transaction committed': (r) => r.status === 201 });
    }
    sleep(Math.random() * 2 + 1);
}

export function readHeavy(data) {
    const [user] = pickPair(data.users);
    check(health(), { 'health ok': (r) => r.status === 200 });
    check(getAccounts(user), { 'accounts retrieved': (r) => r.status === 200 });
    check(history(user), { 'history retrieved': (r) => r.status === 200 });
    sleep(Math.random() + 0.5);
}
