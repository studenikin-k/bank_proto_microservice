// Smoke-тест: функциональная проверка стенда перед нагрузкой.
// Проходит полный путь клиента и проверяет правила банка: баланс после перевода,
// комиссию, идемпотентность, запрет списания с чужого счёта, отказ при нехватке средств.
import http from 'k6/http';
import { check, group } from 'k6';
import { BASE_URL, summaryTrendStats } from '../config.js';
import { authHeaders, createUsers, getAccounts, health, transfer } from '../lib/bank.js';

export const options = {
    scenarios: {
        smoke: { executor: 'per-vu-iterations', vus: 1, iterations: 3, maxDuration: '2m' },
    },
    thresholds: {
        checks: ['rate==1.0'],
        http_req_failed: ['rate==0'],
        http_req_duration: ['p(95)<500'],
    },
    summaryTrendStats,
};

function balanceOf(user) {
    const res = getAccounts(user);
    const acc = res.json('accounts').find((a) => a.account_id === user.account_id);
    return acc ? acc.balance : NaN;
}

const cents = (x) => Math.round(x * 100);

export default function () {
    group('health', () => {
        const res = health();
        check(res, {
            'health 200': (r) => r.status === 200,
            'health status OK': (r) => r.json('status') === 'OK',
        });
    });

    const [alice, bob, carol] = createUsers(3, 'smoke');
    const aliceStart = balanceOf(alice);
    const bobStart = balanceOf(bob);
    check(aliceStart, { 'новый счёт открыт с балансом 20000.00': (b) => b === 20000 });

    group('перевод и идемпотентность', () => {
        const key = `smoke-${__ITER}-${Date.now()}`;
        const body = JSON.stringify({ from_account_id: alice.account_id, to_account_id: bob.account_id, amount: 100, type: 'transfer' });
        const params = { headers: Object.assign(authHeaders(alice.token), { 'Idempotency-Key': key }) };

        const first = http.post(BASE_URL + '/transactions', body, params);
        check(first, {
            'перевод 201': (r) => r.status === 201,
            'перевод completed': (r) => r.json('transaction.status') === 'completed',
            'комиссия 1% = 1.00': (r) => r.json('transaction.fee_amount') === 1,
        });
        const replay = http.post(BASE_URL + '/transactions', body, params);
        check(replay, {
            'повтор с тем же ключом 201': (r) => r.status === 201,
            'повтор вернул ту же операцию': (r) => r.json('transaction.id') === first.json('transaction.id'),
            'повтор помечен Idempotent-Replayed': (r) => r.headers['Idempotent-Replayed'] === 'true',
        });
        check(null, {
            'списано 101.00 один раз': () => cents(balanceOf(alice)) === cents(aliceStart) - 10100,
            'зачислено 100.00 один раз': () => cents(balanceOf(bob)) === cents(bobStart) + 10000,
        });
    });

    group('правила банка', () => {
        const steal = http.post(BASE_URL + '/transactions',
            JSON.stringify({ from_account_id: alice.account_id, to_account_id: bob.account_id, amount: 1 }),
            { headers: authHeaders(bob.token), responseCallback: http.expectedStatuses(403) });
        check(steal, { 'списание с чужого счёта запрещено (403)': (r) => r.status === 403 && r.json('code') === 'ACCOUNT_FORBIDDEN' });

        const tooMuch = transfer(alice, bob, 1000000);
        check(tooMuch, { 'нехватка средств (422)': (r) => r.status === 422 && r.json('code') === 'INSUFFICIENT_FUNDS' });

        const payment = transfer(alice, bob, 12.34, 'payment');
        check(payment, {
            'платёж 201': (r) => r.status === 201,
            'комиссия платежа 3% = 0.37': (r) => r.json('transaction.fee_amount') === 0.37,
        });
    });

    group('история и доступ к операциям', () => {
        const hist = http.get(BASE_URL + '/transactions', { headers: authHeaders(bob.token) });
        const txs = hist.json('transactions') || [];
        check(hist, {
            'история получателя 200': (r) => r.status === 200,
            'получатель видит входящие переводы': () => txs.filter((t) => t.status === 'completed').length >= 2,
        });
        const id = txs.length ? txs[0].id : 'none';
        const foreign = http.get(BASE_URL + '/transactions/' + id,
            { headers: authHeaders(carol.token), responseCallback: http.expectedStatuses(404) });
        check(foreign, { 'чужая операция не видна (404)': (r) => r.status === 404 });
    });
}
