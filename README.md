# Банковский прототип: микросервисная версия

Микросервисная реализация банковского прототипа (монолит — проект `bank-prototype`).
Go, gRPC, PostgreSQL (отдельная БД на сервис), Redis, nginx, k6.

```
                     ┌──────────┐
 клиент ── :80 ────▶ │  nginx   │ ── балансировка least_conn ──┐
        └─ :8080 ─┐  └──────────┘                              ▼
                  └────────────────────────────────▶ ┌──────────────────┐ ×N
                                                     │   API Gateway    │ HTTP → gRPC, JWT
                                                     └──┬──────┬─────┬──┘
                                     ┌──────────────────┘      │     └──────────────┐
                                     ▼                         ▼                    ▼
                              ┌─────────────┐         ┌──────────────────┐  ┌─────────────────┐
                              │ Auth :50051 │         │ Transaction      │─▶│ Account :50052  │
                              └──────┬──────┘         │ :50053 (сага)    │  └───┬─────────┬───┘
                                     ▼                └────────┬─────────┘      ▼         ▼
                               PostgreSQL users         PostgreSQL tx     PostgreSQL   Redis
                                 (:5431)                  (:5433)         accounts     (кеш)
                                                                          (:5432)
```

## Перевод денег: сага с идемпотентностью

Деньги лежат в БД Account Service, журнал операций — в БД Transaction Service.
Одной транзакцией их не обновить, поэтому перевод выполняется как сага:

1. **Transaction Service** создаёт запись `pending`. Её `id` — это `transfer_id`.
2. **Account Service.ApplyTransfer** в одной локальной транзакции:
   - занимает `transfer_id` в журнале `applied_transfers` (повтор с тем же id ничего не спишет);
   - блокирует оба счёта `SELECT … FOR UPDATE` **в порядке возрастания id**, поэтому
     встречные переводы A→B и B→A не могут взаимно заблокироваться;
   - проверяет владельца счёта списания, статусы счетов и баланс;
   - списывает `сумма + комиссия`, зачисляет `сумма`. Комиссия копится в журнале и раз
     в `FEE_SWEEP_INTERVAL` переносится на системный счёт одной транзакцией, поэтому
     системный счёт не становится общей «горячей» строкой для всех переводов.

   У каждого `transfer_id` ровно один окончательный исход: `applied` или `aborted`.
   Отказ (нехватка средств, чужой счёт и т. п.) тоже записывается навсегда.
3. **Transaction Service** помечает запись `completed` или `failed`.

Если исход шага 2 неизвестен (таймаут, обрыв связи, падение процесса), клиент получает
`503/504` с кодом `OUTCOME_UNKNOWN` и `transaction_id`, а запись остаётся `pending`.
Дальше возможны два пути:

- клиент повторяет запрос с тем же заголовком `Idempotency-Key`. Шаг 2 повторяется
  идемпотентно, и перевод доводится до конца;
- **recovery-воркер** (каждые `RECOVERY_INTERVAL`) берёт записи `pending` старше
  `RECOVERY_AGE` и вызывает `ResolveTransfer`. Если перевод применён, запись становится
  `completed`. Иначе `transfer_id` навсегда помечается `aborted`: даже запоздавший запрос
  уже не спишет деньги, а запись становится `failed`.

Итог: при любых сбоях деньги не теряются и не списываются дважды. Это проверяют
интеграционные тесты (`internal/transaction/service/saga_test.go`) с имитацией потери
запросов и ответов, и `make reconcile` на живом стенде.

Асинхронный перевод (`POST /transactions/async`) сразу возвращает `transaction_id`
со статусом `pending` и исполняется Worker Pool. Worker Pool повторяет только вызовы
с неизвестным исходом, а это безопасно благодаря идемпотентности.

Все суммы внутри системы — целые копейки (`int64`, `BIGINT`), без `float64`. В JSON API
суммы — числа в рублях с двумя знаками: `"amount": 12.34`. Новый счёт открывается с
балансом `ACCOUNT_OPENING_BALANCE` (по умолчанию 20000.00 ₽).

## Ошибки API

Ответ с ошибкой: `{"error": "текст", "code": "ПРИЧИНА"[, "transaction_id": "…"]}`.

| HTTP | gRPC | Когда | Примеры `code` |
|---|---|---|---|
| 400 | InvalidArgument | некорректный запрос | `INVALID_ARGUMENT` |
| 401 | Unauthenticated | нет или неверный токен, неверный пароль | `UNAUTHENTICATED`, `BAD_CREDENTIALS` |
| 403 | PermissionDenied | чужой счёт | `ACCOUNT_FORBIDDEN` |
| 404 | NotFound | нет счёта или операции | `ACCOUNT_NOT_FOUND`, `RECIPIENT_NOT_FOUND`, `TRANSACTION_NOT_FOUND` |
| 409 | AlreadyExists, Aborted | конфликт; запрос не выполнен | `USER_EXISTS`, `IDEMPOTENCY_CONFLICT`, `CONCURRENCY_CONFLICT` |
| 422 | FailedPrecondition | нарушено бизнес-правило | `INSUFFICIENT_FUNDS`, `ACCOUNT_CLOSED`, `ACCOUNT_LIMIT_REACHED`, `TRANSFER_ABORTED` |
| 503 | Unavailable, ResourceExhausted | сервис недоступен или перегружен | `SERVICE_UNAVAILABLE`, `OVERLOADED`, `OUTCOME_UNKNOWN` |
| 504 | DeadlineExceeded | таймаут | `TIMEOUT`, `OUTCOME_UNKNOWN` |

4xx — запрос точно не выполнен. После 503/504 на перевод его исход может быть неизвестен;
повтор с тем же `Idempotency-Key` безопасен.

## Запуск

```bash
make up        # стенд с одной репликой шлюза: nginx на :80, шлюз напрямую на :8080
make up-lb     # то же, но 4 реплики шлюза за nginx (GATEWAY_EXTRA_REPLICAS=3)
make down      # остановить (данные БД сохраняются)
make reset     # удалить данные всех БД и поднять стенд заново
make logs
```

Настройки задаются переменными окружения, полный список — в `docker-compose.yml`:
лимиты CPU контейнеров (`GATEWAY_CPUS`, `ACCOUNT_CPUS`, `PG_CPUS`, …), размеры пулов
соединений (`*_DB_MAX_CONNS`), таймауты (`REQUEST_TIMEOUT`, `ACCOUNT_CALL_TIMEOUT`,
`ACCOUNT_DB_LOCK_TIMEOUT`), `CACHE_TTL` (`0` выключает кеш), `LOG_LEVEL`
(`debug` — лог каждого запроса).

Сервисы можно запускать и без Docker (`go run ./cmd/<service>`): значения по умолчанию
указывают на `localhost` и порты БД из docker-compose.

## Тесты

```bash
make test               # юнит-тесты с -race
make test-integration   # + интеграционные тесты на БД стенда (нужен make up)
```

Интеграционные тесты работают во временных схемах PostgreSQL и не трогают данные стенда.
Они проверяют:

- тысячи параллельных переводов с дублями: сумма денег сохраняется, баланс каждого
  счёта сходится с журналом;
- встречные переводы без взаимоблокировок (по `pg_stat_database.deadlocks`);
- идемпотентность, окончательность отказов, запрет на чужой счёт, закрытые счета,
  лимит в 5 счетов при параллельных запросах;
- сагу: потерю ответа, потерю запроса, recovery, асинхронные повторы, сотни переводов
  со случайными сбоями.

`make reconcile` сверяет БД живого стенда: `Σ выданных денег = Σ балансов + Σ ещё
не перенесённых комиссий`, `completed ⇔ деньги переведены`, `failed ⇒ не переведены`.

## Нагрузочное тестирование

Основной сравнительный набор smoke, capacity, stress и spike находится в
`load-tests/comparison`. Порядок запуска и условия серии описаны в
[COURSE_PLAN.md](load-tests/comparison/COURSE_PLAN.md):

```bash
bash load-tests/comparison/course-suite.sh prepare
ALLOW_STALE_READS=1 bash load-tests/comparison/course-suite.sh all
```

Для отчёта сохранена завершённая серия
`load-tests/results/course/all-20261005T131233Z-38840` из 40 запусков.
Готовый отчёт, рисунки и сводные данные находятся в `course-report`.
Сырые результаты хранятся локально и исключены из Git.

Дополнительные сценарии для обычного стенда находятся в `load-tests/scenarios`.
Адрес всегда задаётся явно:
`-e BASE_URL=http://localhost:8080` (шлюз напрямую) или `http://localhost` (через nginx).

Метрики k6:

- `http_req_failed` — только сбои: сеть и 5xx;
- `transfer_committed` — доля успешных переводов;
- `transfer_rejected{code:…}` — бизнес-отказы 4xx по причинам;
- `transfer_unknown` — 503/504 с неизвестным исходом.

| Сценарий | Модель | Что показывает |
|---|---|---|
| `smoke-test.js` | 1 VU | функциональная проверка правил банка перед нагрузкой |
| `capacity.js` + `run-capacity.sh` | открытая (constant-arrival-rate) | **пропускную способность**: ступени нагрузки до точки насыщения |
| `run-failover.sh` | открытая | отказоустойчивость: отказ реплики шлюза или сервиса под нагрузкой |

```bash
k6 run -e BASE_URL=http://localhost:8080 load-tests/scenarios/smoke-test.js
WORKLOAD=transfer LABEL=micro-1gw ./load-tests/run-capacity.sh
WORKLOAD=mixed LABEL=micro-4gw BASE_URL=http://localhost ./load-tests/run-capacity.sh
MODE=nginx ./load-tests/run-failover.sh                     # отказ 1 из 4 реплик за nginx
MODE=direct ./load-tests/run-failover.sh                    # отказ единственного шлюза
TARGET=account MODE=direct ./load-tests/run-failover.sh     # падение Account Service: проверка саги
```

`run-capacity.sh` для каждой ступени сохраняет итог k6, загрузку CPU каждого контейнера
(`docker stats`), гистограммы латентности сервисов и счётчики PostgreSQL. Ступень, на
которой достигнутый RPS отстаёт от заданного или p99 выходит за `SLO_P99_MS`, помечается
`SATURATED`. Контейнер с максимальной загрузкой CPU на этой ступени — узкое место.
Нагрузки (`WORKLOAD`): `read`, `transfer` (200 счетов, мало конкуренции), `hot`
(20 счетов, конкуренция за строки), `mixed` (70% чтений), `login` (bcrypt), `history`.

Метрики сервисов (стандартные `expvar` и `pprof`, отдельный порт 6060 внутри контейнера):

```bash
docker compose exec account wget -qO- http://127.0.0.1:6060/debug/vars
```

Здесь гистограммы латентности каждого gRPC-метода и HTTP-маршрута, статистика пулов
соединений PostgreSQL, попадания в кеш и счётчики Worker Pool. Сравнив гистограммы
шлюза, Transaction Service и Account Service, можно разложить время перевода по хопам.

Для сопоставления с монолитом используйте сравнительный профиль из `load-tests/comparison`
и его план запуска. Отдельные capacity/failover-сценарии предназначены для дополнительных
экспериментов с обычным стендом; их результаты не объединяются с серией отчёта.
