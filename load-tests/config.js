export const BASE_URL = __ENV.BASE_URL || 'http://localhost:8080';

export const thresholds = {
    http_req_duration: ['p(95)<500', 'p(99)<1000'],
    http_req_failed: ['rate<0.01'],
    http_reqs: ['rate>100'],
};

export const summaryTrendStats = ['avg', 'min', 'med', 'max', 'p(90)', 'p(95)', 'p(99)'];

export function generateRandomUser() {
    const timestamp = Date.now();
    const random = Math.floor(Math.random() * 10000);
    return {
        name: 'user_' + timestamp + '_' + random,
        password: 'Pass' + random + '!@#'
    };
}

export function generateRandomAmount(min, max) {
    min = min || 1;
    max = max || 100;
    return parseFloat((Math.random() * (max - min) + min).toFixed(2));
}

