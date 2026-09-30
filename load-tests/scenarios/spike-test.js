import http from 'k6/http';
import { check, sleep } from 'k6';
import { BASE_URL } from '../config.js';

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
        http_req_failed: ['rate<0.20'],
    },
};

export function setup() {
    console.log('Preparing spike test...');
    const users = [];

    for (let i = 0; i < 30; i++) {
        const timestamp = Date.now();
        const random = Math.floor(Math.random() * 100000) + i;
        const username = 'spikeuser_' + timestamp + '_' + random;
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
                            users.push({
                                username: username,
                                password: password,
                                token: token,
                                account_id: accountBody.account_id,
                            });
                        } catch (e) {
                            console.log('Failed to parse account response');
                        }
                    }
                } catch (e) {
                    console.log('Failed to parse login response');
                }
            }
        }

        sleep(0.1);
    }

    console.log('Spike test ready with ' + users.length + ' users');
    return { users: users };
}

export default function (data) {
    if (!data || !data.users || data.users.length === 0) {
        return;
    }

    const user = data.users[Math.floor(Math.random() * data.users.length)];
    const authHeader = { 'Authorization': 'Bearer ' + user.token };

    http.get(BASE_URL + '/health');

    const accountsRes = http.get(BASE_URL + '/accounts', { headers: authHeader });
    check(accountsRes, {
        'health ok': (r) => r.status === 200,
        'accounts retrieved': (r) => r.status === 200,
    });

    const randomAmount = parseFloat((Math.random() * 20 + 5).toFixed(2));
    const targetUser = data.users[Math.floor(Math.random() * data.users.length)];

    if (targetUser && targetUser.account_id !== user.account_id) {
        const transferPayload = JSON.stringify({
            from_account_id: user.account_id,
            to_account_id: targetUser.account_id,
            amount: randomAmount,
            type: 'transfer',
        });

        const transferRes = http.post(BASE_URL + '/transactions', transferPayload, {
            headers: Object.assign({}, authHeader, { 'Content-Type': 'application/json' }),
        });

        check(transferRes, {
            'transfer ok': (r) => r.status === 201 || r.status === 400,
        });
    }

    sleep(0.3);
}


