.PHONY: proto build test test-integration up up-lb down reset logs reconcile smoke capacity

PROTO_DIR = proto

# Интеграционные тесты создают во временных схемах собственные таблицы,
# поэтому им подходят базы запущенного docker-compose.
ACCOUNT_TEST_DATABASE_URL ?= postgres://account_user:account_password@localhost:5432/account_db?sslmode=disable
TX_TEST_DATABASE_URL ?= postgres://tx_user:tx_password@localhost:5433/tx_db?sslmode=disable

proto:
	protoc --proto_path=$(PROTO_DIR) \
		--go_out=$(PROTO_DIR) --go_opt=paths=source_relative \
		--go-grpc_out=$(PROTO_DIR) --go-grpc_opt=paths=source_relative \
		$(PROTO_DIR)/auth/*.proto \
		$(PROTO_DIR)/account/*.proto \
		$(PROTO_DIR)/transaction/*.proto

build:
	go build ./...

# Юнит-тесты (без БД).
test:
	go test -race ./...

# Юнит- и интеграционные тесты: нужен запущенный `make up`.
test-integration:
	ACCOUNT_TEST_DATABASE_URL='$(ACCOUNT_TEST_DATABASE_URL)' TX_TEST_DATABASE_URL='$(TX_TEST_DATABASE_URL)' \
		go test -race -count=1 ./...

# Стенд с одной репликой шлюза.
up:
	docker compose up -d --build --wait

# Стенд с четырьмя репликами шлюза за nginx.
up-lb:
	GATEWAY_EXTRA_REPLICAS=3 docker compose up -d --build --wait

down:
	docker compose down

# Удалить данные всех БД и поднять стенд заново.
reset:
	docker compose down -v
	docker compose up -d --build --wait

logs:
	docker compose logs -f --tail=100

# Сверка БД счетов и БД транзакций.
reconcile:
	go run ./cmd/reconcile

smoke:
	k6 run load-tests/scenarios/smoke-test.js

# Поиск точки насыщения, пример: make capacity WORKLOAD=transfer LABEL=micro-1gw
capacity:
	./load-tests/run-capacity.sh
