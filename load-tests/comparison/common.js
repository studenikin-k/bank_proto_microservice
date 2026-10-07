import http from 'k6/http';
import { fail } from 'k6';
export function integer(name, fallback, min = 1) {
    const value = Number(__ENV[name] || fallback);
    if (!Number.isInteger(value) || value < min) throw new Error(name + ': invalid integer');
    return value;
}
export const BASE_URL = (__ENV.BASE_URL || '').replace(/\/$/, '');
if (!/^https?:\/\//.test(BASE_URL)) throw new Error('BASE_URL must be explicit');
export const USERS = integer('USERS', 200, 2);
export const SEED = integer('SEED', 42, 0);
export const TIMEOUT = integer('REQUEST_TIMEOUT_SECONDS', 10);
export const STATS = ['avg', 'min', 'med', 'p(90)', 'p(95)', 'p(99)', 'max'];
export const cents = (x) => typeof x === 'number' && Number.isFinite(x) ? Math.round(x * 100) : NaN;
export const accountID = (a) => a && (a.account_id || a.id);
export function json(res) { try { return res.json(); } catch (_) { return null; } }
export function params(user, name, expected = [200]) {
    return { headers: { 'Content-Type': 'application/json', ...(user ? { Authorization: 'Bearer ' + user.token } : {}) },
        timeout: TIMEOUT + 's', tags: { name }, responseCallback: http.expectedStatuses(...expected) };
}
export function request(method, path, body, user, name, expected = [200]) {
    return http.request(method, BASE_URL + path, body === null ? null : JSON.stringify(body), params(user, name, expected));
}
export function createUsers(count = USERS) {
    const users = [], prefix = (__ENV.RUN_ID || 'manual') + '-' + Date.now();
    for (let i = 0; i < count; i++) {
        const name = 'compare-' + prefix + '-' + i, password = 'Compare-pass-42!';
        const r = request('POST', '/register', { name, password }, null, 'setup_register', [201]);
        if (r.status !== 201 || !json(r) || !json(r).user_id) fail('setup registration failed: ' + r.status);
        const l = request('POST', '/login', { name, password }, null, 'setup_login'), lb = json(l);
        if (l.status !== 200 || !lb || !lb.token) fail('setup login failed: ' + l.status);
        const u = { name, password, token: lb.token };
        const a = request('POST', '/accounts', {}, u, 'setup_account', [201]), ab = json(a);
        if (a.status !== 201 || !accountID(ab) || cents(ab.balance) !== 2000000 || ab.status !== 'active')
            fail('setup needs one active account with 20000.00 RUB for every user');
        u.account_id = accountID(ab);
        users.push(u);
    }
    return { users };
}
export function getAccounts(user) { return request('GET', '/accounts', null, user, 'accounts'); }
export function validAccounts(res, user) {
    const b = json(res);
    return res.status === 200 && b && Array.isArray(b.accounts) && b.accounts.length === 1 &&
        b.accounts.some((a) => accountID(a) === user.account_id && a.status === 'active' &&
            Number.isFinite(a.balance) && cents(a.balance) >= 0);
}
export function transfer(from, to, amount, type, key, expected = [201], actor = from) {
    const p = params(actor, 'transaction_' + type, expected);
    p.headers['Idempotency-Key'] = key; // unique keys, never replay in the common workload
    return http.post(BASE_URL + '/transactions', JSON.stringify({
        from_account_id: from.account_id, to_account_id: to.account_id, amount, type
    }), p);
}
export function validTransfer(res, from, to, amount, type) {
    const b = json(res), t = b && b.transaction;
    const fee = Math.round(cents(amount) * (type === 'payment' ? 3 : 1) / 100);
    return res.status === 201 && t && typeof t.id === 'string' && t.id.length > 0 &&
        t.status === 'completed' && t.type === type &&
        t.from_account_id === from.account_id && t.to_account_id === to.account_id &&
        cents(t.amount) === cents(amount) && cents(t.fee_amount) === fee &&
        cents(t.total_debit) === cents(amount) + fee && t.fee_percent === (type === 'payment' ? 3 : 1);
}
function hash(n) {
    let x = (n + SEED) >>> 0;
    x = Math.imul(x ^ (x >>> 16), 0x45d9f3b);
    x = Math.imul(x ^ (x >>> 16), 0x45d9f3b);
    return (x ^ (x >>> 16)) >>> 0;
}
export function choice(i, workload, count) {
    const isTransfer = workload === 'transfer' || workload === 'hot' || (workload === 'mixed' && i % 10 >= 7);
    const index = hash(i * 2) % count, other = (index + 1 + hash(i * 2 + 1) % (count - 1)) % count;
    const ti = workload === 'mixed' ? Math.floor(i / 10) * 3 + i % 10 - 7 : i;
    return { isTransfer, index, other, type: ti % 10 < 7 ? 'transfer' : 'payment' };
}
