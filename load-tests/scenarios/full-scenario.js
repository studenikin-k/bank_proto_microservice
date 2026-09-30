import http from 'k6/http';
import { check, sleep } from 'k6';
import { BASE_URL } from '../config.js';
import exec from 'k6/execution';

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
        http_req_failed: ['rate<0.05'],
    },
};

const sharedUsers = [];

export function userRegistration() {
    const timestamp = Date.now();
    const vuId = exec.vu.idInTest;
    const random = Math.floor(Math.random() * 100000);
    const username = 'fulluser_' + timestamp + '_' + vuId + '_' + random;
    const password = 'Pass' + random + '!@#';

    const registerPayload = JSON.stringify({
        name: username,
        password: password,
    });

    const registerRes = http.post(BASE_URL + '/register', registerPayload, {
        headers: { 'Content-Type': 'application/json' },
    });

    if (registerRes.status === 201) {
        const loginPayload = JSON.stringify({
            name: username,
            password: password,
        });

        const loginRes = http.post(BASE_URL + '/login', loginPayload, {
            headers: { 'Content-Type': 'application/json' },
        });

        if (loginRes.status === 200) {
            try {
                const loginBody = JSON.parse(loginRes.body);
                const token = loginBody.token;

                const accountRes = http.post(BASE_URL + '/accounts', '{}', {
                    headers: {
                        'Content-Type': 'application/json',
                        'Authorization': 'Bearer ' + token,
                    },
                });

                if (accountRes.status === 201) {
                    try {
                        const accountBody = JSON.parse(accountRes.body);
                        sharedUsers.push({
                            username: username,
                            password: password,
                            token: token,
                            account_id: accountBody.account_id,
                        });
                    } catch (e) {
                        console.log('Failed to parse account');
                    }
                }
            } catch (e) {
                console.log('Failed to parse login');
            }
        }
    }

    sleep(Math.random() * 3 + 2);
}

export function bankingOperations() {
    if (sharedUsers.length === 0) {
        sleep(2);
        return;
    }

    const user = sharedUsers[Math.floor(Math.random() * sharedUsers.length)];
    const authHeader = { 'Authorization': 'Bearer ' + user.token };

    const accountsRes = http.get(BASE_URL + '/accounts', { headers: authHeader });
    check(accountsRes, {
        'accounts retrieved': (r) => r.status === 200,
    });

    if (Math.random() > 0.3 && sharedUsers.length > 1) {
        const targetUser = sharedUsers[Math.floor(Math.random() * sharedUsers.length)];
        if (targetUser.account_id !== user.account_id) {
            const amount = parseFloat((Math.random() * 30 + 10).toFixed(2));
            const transactionType = Math.random() > 0.6 ? 'transfer' : 'payment';

            const transactionPayload = JSON.stringify({
                from_account_id: user.account_id,
                to_account_id: targetUser.account_id,
                amount: amount,
                type: transactionType,
            });

            const transactionRes = http.post(BASE_URL + '/transactions', transactionPayload, {
                headers: Object.assign({}, authHeader, { 'Content-Type': 'application/json' }),
            });

            check(transactionRes, {
                'transaction completed': (r) => r.status === 201 || r.status === 400,
            });
        }
    }

    sleep(Math.random() * 2 + 1);
}

export function readHeavy() {
    if (sharedUsers.length === 0) {
        sleep(1);
        return;
    }

    const user = sharedUsers[Math.floor(Math.random() * sharedUsers.length)];
    const authHeader = { 'Authorization': 'Bearer ' + user.token };

    http.get(BASE_URL + '/health');
    http.get(BASE_URL + '/accounts', { headers: authHeader });

    sleep(Math.random() * 1 + 0.5);
}


