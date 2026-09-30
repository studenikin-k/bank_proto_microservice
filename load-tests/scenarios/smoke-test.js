import http from 'k6/http';
import { check, sleep } from 'k6';
import { BASE_URL } from '../config.js';

export const options = {
    stages: [
        { duration: '30s', target: 1 },
        { duration: '30s', target: 1 },
    ],
    thresholds: {
        http_req_duration: ['p(95)<500'],
        http_req_failed: ['rate<0.01'],
    },
};

export default function () {
    const timestamp = Date.now();
    const random = Math.floor(Math.random() * 10000);
    const username = 'user_' + timestamp + '_' + random;
    const password = 'Pass' + random + '!@#';

    const healthRes = http.get(BASE_URL + '/health');
    check(healthRes, {
        'health check status is 200': (r) => r.status === 200,
        'health check has OK status': (r) => {
            try {
                const body = JSON.parse(r.body);
                return body.status === 'OK';
            } catch (e) {
                return false;
            }
        },
    });

    const registerPayload = JSON.stringify({
        name: username,
        password: password,
    });

    const registerRes = http.post(BASE_URL + '/register', registerPayload, {
        headers: { 'Content-Type': 'application/json' },
    });

    check(registerRes, {
        'registration status is 201': (r) => r.status === 201,
        'registration returns user_id': (r) => {
            try {
                const body = JSON.parse(r.body);
                return body.user_id && body.user_id.length > 0;
            } catch (e) {
                return false;
            }
        },
    });

    const loginPayload = JSON.stringify({
        name: username,
        password: password,
    });

    const loginRes = http.post(BASE_URL + '/login', loginPayload, {
        headers: { 'Content-Type': 'application/json' },
    });

    let token = '';
    check(loginRes, {
        'login status is 200': (r) => r.status === 200,
        'login returns token': (r) => {
            try {
                const body = JSON.parse(r.body);
                token = body.token || '';
                return token.length > 0;
            } catch (e) {
                return false;
            }
        },
    });

    if (token) {
        const createAccountRes = http.post(
            BASE_URL + '/accounts',
            '{}',
            {
                headers: {
                    'Content-Type': 'application/json',
                    'Authorization': 'Bearer ' + token,
                },
            }
        );

        check(createAccountRes, {
            'account creation status is 201': (r) => r.status === 201,
            'account has ID': (r) => {
                try {
                    const body = JSON.parse(r.body);
                    return body.account_id && body.account_id.length > 0;
                } catch (e) {
                    return false;
                }
            },
        });

        const accountsRes = http.get(BASE_URL + '/accounts', {
            headers: {
                'Authorization': 'Bearer ' + token,
            },
        });

        check(accountsRes, {
            'accounts list status is 200': (r) => r.status === 200,
            'accounts list is not empty': (r) => {
                try {
                    const body = JSON.parse(r.body);
                    return body.accounts && body.accounts.length > 0;
                } catch (e) {
                    return false;
                }
            },
        });
    }

    sleep(1);
}


