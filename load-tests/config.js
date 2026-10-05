// Общие настройки сценариев. Адрес стенда задаётся явно при каждом запуске:
//   k6 run -e BASE_URL=http://localhost:8080 ...   — один шлюз напрямую
//   k6 run -e BASE_URL=http://localhost ...        — через nginx
export const BASE_URL = __ENV.BASE_URL || 'http://localhost:8080';

export const summaryTrendStats = ['avg', 'min', 'med', 'p(90)', 'p(95)', 'p(99)', 'max'];

export function randomAmount(min, max) {
    return Number((Math.random() * (max - min) + min).toFixed(2));
}
