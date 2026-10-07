// Клиент банковского API для k6-сценариев.
//
// Как считаются ошибки:
//   http_req_failed      — только сбои: сетевые ошибки и ответы 5xx;
//   transfer_committed   — доля переводов, завершившихся успехом (201);
//   transfer_rejected    — определённые отказы 4xx (нехватка средств, конфликт блокировок…),
//                          с тегом code — машиночитаемой причиной из ответа;
//   transfer_unknown     — 503/504 «исход неизвестен»: перевод доведёт recovery-воркер.
// Отказ 4xx — корректное поведение банка, а не сбой сервера, поэтому он не попадает
// в http_req_failed, но имеет свой порог в сценариях.
import http from 'k6/http';
import { fail } from 'k6';
import { Counter, Rate } from 'k6/metrics';
import { BASE_URL } from '../config.js';

export const transferCommitted = new Rate('transfer_committed');
export const transferRejected = new Counter('transfer_rejected');
export const transferUnknown = new Counter('transfer_unknown');

const JSON_HEADERS = { 'Content-Type': 'application/json' };
const TRANSFER_EXPECTED = http.expectedStatuses({ min: 200, max: 299 }, 403, 404, 409, 422);

// По умолчанию «успех» — только 2xx: 4xx на чтении или регистрации — это сбой сценария.
http.setResponseCallback(http.expectedStatuses({ min: 200, max: 299 }));

export function authHeaders(token) {
    return { Authorization: 'Bearer ' + token, 'Content-Type': 'application/json' };
}

function accountIdOf(res) {
    try {
        const body = res.json();
        return body.account_id || body.id; // монолит отдаёт только id
    } catch (e) {
        return undefined;
    }
}

// createUsers регистрирует n пользователей, логинит их и открывает каждому счёт.
// Запросы идут пачками через http.batch, поэтому даже сотни пользователей
// создаются за секунды. Если создать не удалось хотя бы 95%, тест прерывается:
// нагрузка на неподготовленном стенде ничего не измеряет.
export function createUsers(n, prefix) {
    const users = [];
    const batchSize = 25;
    const params = { headers: JSON_HEADERS, tags: { name: 'setup' }, responseType: 'text' };
    for (let start = 0; start < n; start += batchSize) {
        const creds = [];
        for (let i = start; i < Math.min(n, start + batchSize); i++) {
            creds.push({
                name: `${prefix}_${Date.now()}_${i}_${Math.floor(Math.random() * 1e6)}`,
                password: 'Pass' + Math.floor(Math.random() * 1e6) + '!',
            });
        }
        http.batch(creds.map((c) => ['POST', BASE_URL + '/register', JSON.stringify(c), params]));
        const logins = http.batch(creds.map((c) => ['POST', BASE_URL + '/login', JSON.stringify(c), params]));

        const ready = [];
        logins.forEach((res, i) => {
            if (res.status === 200) {
                ready.push(Object.assign({ token: res.json('token') }, creds[i]));
            }
        });
        const accounts = http.batch(ready.map((u) => ['POST', BASE_URL + '/accounts', '{}',
            { headers: authHeaders(u.token), tags: { name: 'setup' }, responseType: 'text' }]));
        accounts.forEach((res, i) => {
            const accountId = res.status === 201 ? accountIdOf(res) : undefined;
            if (accountId) {
                users.push({ name: ready[i].name, password: ready[i].password, token: ready[i].token, account_id: accountId });
            }
        });
    }
    if (users.length < n * 0.95) {
        fail(`создано ${users.length} из ${n} пользователей со счетами — проверьте стенд (${BASE_URL})`);
    }
    if (n > 1) {
        console.log(`подготовлено пользователей со счетами: ${users.length}`);
    }
    return users;
}

export function pick(list) {
    return list[Math.floor(Math.random() * list.length)];
}

// pickPair возвращает отправителя и другого получателя.
export function pickPair(users) {
    const from = pick(users);
    let to = pick(users);
    while (to.account_id === from.account_id && users.length > 1) {
        to = pick(users);
    }
    return [from, to];
}

export function getAccounts(user) {
    return http.get(BASE_URL + '/accounts', {
        headers: authHeaders(user.token),
        tags: { name: 'GET /accounts' },
    });
}

export function health() {
    return http.get(BASE_URL + '/health', { tags: { name: 'GET /health' } });
}

let keySeq = 0;

// transfer выполняет перевод или платёж с уникальным ключом идемпотентности.
export function transfer(from, to, amount, type) {
    const res = http.post(BASE_URL + '/transactions', JSON.stringify({
        from_account_id: from.account_id,
        to_account_id: to.account_id,
        amount: amount,
        type: type || 'transfer',
    }), {
        headers: Object.assign(authHeaders(from.token), { 'Idempotency-Key': `k6-${__VU}-${__ITER}-${keySeq++}-${Date.now()}` }),
        tags: { name: 'POST /transactions' },
        responseCallback: TRANSFER_EXPECTED,
    });

    transferCommitted.add(res.status === 201);
    if (res.status >= 400 && res.status < 500) {
        let code = String(res.status);
        try {
            code = res.json('code') || code;
        } catch (e) {
            // тело не JSON — оставляем HTTP-код
        }
        transferRejected.add(1, { code: code });
    } else if (res.status === 503 || res.status === 504) {
        transferUnknown.add(1);
    }
    return res;
}

export function login(user) {
    return http.post(BASE_URL + '/login', JSON.stringify({ name: user.name, password: user.password }), {
        headers: JSON_HEADERS,
        tags: { name: 'POST /login' },
    });
}

export function history(user) {
    return http.get(BASE_URL + '/transactions?limit=20', {
        headers: authHeaders(user.token),
        tags: { name: 'GET /transactions' },
    });
}
