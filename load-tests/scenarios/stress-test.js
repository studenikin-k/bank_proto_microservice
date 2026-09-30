import http from 'k6/http';
import { check, sleep } from 'k6';
import { BASE_URL } from '../config.js';

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
        http_req_failed: ['rate<0.10'],
    },
    setupTimeout: '3m',
};

export function setup() {
    console.log('Creating stress test users...');
    const users = [];

    for (let i = 0; i < 60; i++) {
        const timestamp = Date.now();
        const random = Math.floor(Math.random() * 100000) + i;
        const username = 'stressuser_' + timestamp + '_' + random;
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

        sleep(0.05);
    }

    console.log('Created ' + users.length + ' users with accounts');
    return { users: users };
}

export default function (data) {
    if (!data || !data.users || data.users.length === 0) {
        return;
    }

    const user = data.users[Math.floor(Math.random() * data.users.length)];
    const authHeader = { 'Authorization': 'Bearer ' + user.token };

    const accountsRes = http.get(BASE_URL + '/accounts', { headers: authHeader });
    check(accountsRes, {
        'accounts retrieved': (r) => r.status === 200,
    });

    const randomAmount = parseFloat((Math.random() * 30 + 5).toFixed(2));
    const targetUser = data.users[Math.floor(Math.random() * data.users.length)];

    if (targetUser && targetUser.account_id !== user.account_id) {
        const transactionType = Math.random() > 0.5 ? 'transfer' : 'payment';
        const transactionPayload = JSON.stringify({
            from_account_id: user.account_id,
            to_account_id: targetUser.account_id,
            amount: randomAmount,
            type: transactionType,
        });

        const transactionRes = http.post(BASE_URL + '/transactions', transactionPayload, {
            headers: Object.assign({}, authHeader, { 'Content-Type': 'application/json' }),
        });

        check(transactionRes, {
            'transaction processed': (r) => r.status === 201 || r.status === 400,
        });
    }

    sleep(Math.random() * 1.5 + 0.5);
}


