import { check } from 'k6';
import { Rate } from 'k6/metrics';
import { createUsers, request, getAccounts, validAccounts, transfer, validTransfer, json, cents, STATS } from './common.js';
const fresh = new Rate('read_after_write_fresh');
export const options = {
    scenarios: { smoke: { executor: 'shared-iterations', vus: 1, iterations: 1, maxDuration: '3m' } },
    thresholds: { checks: ['rate==1'], http_req_failed: ['rate==0'] }, summaryTrendStats: STATS,
};
export default function () {
    const { users: [a, b] } = createUsers(2);
    const health = request('GET', '/health', null, null, 'health');
    check(health, { 'health 200 and OK': (r) => r.status === 200 && json(r) && json(r).status === 'OK' });
    const ar = getAccounts(a), br = getAccounts(b);
    check(ar, { 'accounts schema and identity': (r) => Boolean(validAccounts(r, a)) });
    check(br, { 'recipient schema and identity': (r) => Boolean(validAccounts(r, b)) });
    for (const [amount, type] of [[100, 'transfer'], [10, 'payment']]) {
        const r = transfer(a, b, amount, type, 'smoke-' + type + '-' + Date.now());
        check(r, { '201 completed and exact amount/fee': () => Boolean(validTransfer(r, a, b, amount, type)) });
    }
    const balance = (u) => {
        const body = json(getAccounts(u));
        const a = body && body.accounts && body.accounts.find((v) => (v.account_id || v.id) === u.account_id);
        return a ? cents(a.balance) : NaN;
    };
    fresh.add(balance(a) === 1988870 && balance(b) === 2011000);
    const foreign = transfer(a, b, 1, 'transfer', 'smoke-forbidden', [403], b);
    check(foreign, { 'foreign debit denied 403': (r) => r.status === 403 });
    const funds = transfer(a, b, 1000000, 'transfer', 'smoke-funds', [422]);
    check(funds, { 'insufficient funds 422': (r) => r.status === 422 });
    const zero = transfer(a, b, 0, 'transfer', 'smoke-zero', [400]);
    check(zero, { 'zero amount rejected 400': (r) => r.status === 400 });
    const hist = request('GET', '/transactions', null, b, 'history'), hb = json(hist);
    check(hist, { 'recipient sees both completed transactions': (r) => r.status === 200 &&
        hb && Array.isArray(hb.transactions) && hb.transactions.filter((t) => t.status === 'completed').length === 2 });
}
export function handleSummary(data) {
    return { [__ENV.SUMMARY_FILE || 'smoke-summary.json']: JSON.stringify(data, null, 2),
        stdout: 'Smoke saved; read_after_write_fresh is a separate consistency diagnostic.\n' };
}
