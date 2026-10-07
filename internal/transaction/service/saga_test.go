package service_test

// Интеграционные тесты саги: настоящий Account Service (gRPC через bufconn) и две БД.
// Между сервисами стоит клиент, который по команде теряет запрос или ответ, —
// так проверяется, что при сбоях деньги не теряются и не списываются дважды.
// Нужен PostgreSQL: make up && make test-integration.

import (
	"context"
	"math/rand"
	"net"
	"sync"
	"testing"
	"time"

	accgrpc "bank_proto_microservice/internal/account/delivery/grpc"
	accmodels "bank_proto_microservice/internal/account/models"
	accrepo "bank_proto_microservice/internal/account/repository"
	accservice "bank_proto_microservice/internal/account/service"
	"bank_proto_microservice/internal/apperr"
	"bank_proto_microservice/internal/money"
	"bank_proto_microservice/internal/testutil"
	"bank_proto_microservice/internal/transaction/models"
	txrepo "bank_proto_microservice/internal/transaction/repository"
	"bank_proto_microservice/internal/transaction/service"
	"bank_proto_microservice/internal/transaction/worker"
	"bank_proto_microservice/internal/utils"
	accountpb "bank_proto_microservice/proto/account"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"
	"google.golang.org/grpc/test/bufconn"
)

type fault int

const (
	noFault      fault = iota
	dropRequest        // запрос не дошёл до Account Service
	dropResponse       // Account Service выполнил перевод, но ответ потерялся
)

// faultyAccounts — клиент Account Service, который теряет запросы или ответы по сценарию.
type faultyAccounts struct {
	accountpb.AccountServiceClient
	mu     sync.Mutex
	queue  []fault // сбои для следующих вызовов ApplyTransfer
	random float64 // вероятность случайного сбоя, если очередь пуста
	rng    *rand.Rand
}

func (f *faultyAccounts) inject(faults ...fault) {
	f.mu.Lock()
	f.queue = append(f.queue, faults...)
	f.mu.Unlock()
}

func (f *faultyAccounts) next() fault {
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.queue) > 0 {
		x := f.queue[0]
		f.queue = f.queue[1:]
		return x
	}
	if f.random > 0 && f.rng.Float64() < f.random {
		return fault(1 + f.rng.Intn(2))
	}
	return noFault
}

func (f *faultyAccounts) ApplyTransfer(ctx context.Context, in *accountpb.ApplyTransferRequest, opts ...grpc.CallOption) (*accountpb.ApplyTransferResponse, error) {
	switch f.next() {
	case dropRequest:
		return nil, status.Error(codes.Unavailable, "имитация сбоя: запрос потерян")
	case dropResponse:
		_, _ = f.AccountServiceClient.ApplyTransfer(ctx, in, opts...)
		return nil, status.Error(codes.Unavailable, "имитация сбоя: ответ потерян")
	}
	return f.AccountServiceClient.ApplyTransfer(ctx, in, opts...)
}

type sagaEnv struct {
	accPool, txPool *pgxpool.Pool
	accRepo         *accrepo.AccountRepository
	accounts        accountpb.AccountServiceClient // без сбоев
	faults          *faultyAccounts
	svc             *service.TransactionService
	alice, bob      *accmodels.Account
}

func newSagaEnv(t *testing.T) *sagaEnv {
	t.Helper()
	accPool := testutil.PostgresSchema(t, "ACCOUNT_TEST_DATABASE_URL", "migrations/account/000001_init_account.up.sql")
	txPool := testutil.PostgresSchema(t, "TX_TEST_DATABASE_URL", "migrations/transaction/000001_init_transaction.up.sql")

	repo := accrepo.NewAccountRepository(accPool)
	lis := bufconn.Listen(1 << 20)
	srv, _ := utils.NewGRPCServer()
	accountpb.RegisterAccountServiceServer(srv, accgrpc.NewAccountServer(accservice.NewAccountService(repo, nil, money.Rubles(1000))))
	go func() { _ = srv.Serve(lis) }()
	t.Cleanup(srv.Stop)

	conn, err := grpc.NewClient("passthrough:///bufnet",
		grpc.WithContextDialer(func(ctx context.Context, _ string) (net.Conn, error) { return lis.DialContext(ctx) }),
		grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })

	client := accountpb.NewAccountServiceClient(conn)
	faults := &faultyAccounts{AccountServiceClient: client, rng: rand.New(rand.NewSource(7))}
	pool := worker.NewWorkerPool(8, 100, 3)
	pool.Start()
	t.Cleanup(func() { _ = pool.Shutdown(5 * time.Second) })

	env := &sagaEnv{
		accPool: accPool, txPool: txPool, accRepo: repo, accounts: client, faults: faults,
		svc: service.NewTransactionService(txrepo.NewTransactionRepository(txPool), faults, pool,
			service.Config{AccountCallTimeout: 2 * time.Second}),
	}
	env.alice = env.openAccount(t, uuid.NewString())
	env.bob = env.openAccount(t, uuid.NewString())
	return env
}

func (e *sagaEnv) openAccount(t *testing.T, userID string) *accmodels.Account {
	t.Helper()
	acc, err := e.accRepo.Create(context.Background(), userID, money.Rubles(1000), 5)
	if err != nil {
		t.Fatal(err)
	}
	return acc
}

func (e *sagaEnv) balance(t *testing.T, acc *accmodels.Account) money.Amount {
	t.Helper()
	a, err := e.accRepo.GetByID(context.Background(), acc.ID)
	if err != nil {
		t.Fatal(err)
	}
	return a.Balance
}

func (e *sagaEnv) status(t *testing.T, id string) string {
	t.Helper()
	var s string
	if err := e.txPool.QueryRow(context.Background(), `SELECT status FROM transactions WHERE id = $1`, id).Scan(&s); err != nil {
		t.Fatal(err)
	}
	return s
}

func (e *sagaEnv) transfer(key string, from, to *accmodels.Account, amount money.Amount) service.CreateRequest {
	return service.CreateRequest{
		UserID: from.UserID, IdempotencyKey: key, Type: models.TypeTransfer,
		FromAccountID: from.ID, ToAccountID: to.ID, Amount: amount,
	}
}

// recoverAll доводит до конца все pending-записи, как это делает recovery-воркер.
func (e *sagaEnv) recoverAll(t *testing.T) {
	t.Helper()
	time.Sleep(10 * time.Millisecond) // записи должны стать «старше» нулевого порога
	for {
		n, err := e.svc.RecoverStale(context.Background(), 0, 100)
		if err != nil {
			t.Fatal(err)
		}
		if n == 0 {
			return
		}
	}
}

// assertConsistent проверяет сохранность денег и согласованность двух БД.
func (e *sagaEnv) assertConsistent(t *testing.T) {
	t.Helper()
	ctx := context.Background()
	var opening, balances, unswept int64
	if err := e.accPool.QueryRow(ctx, `
		SELECT SUM(opening_balance), SUM(balance),
		       (SELECT COALESCE(SUM(fee), 0) FROM applied_transfers WHERE status = 'applied' AND NOT fee_swept)
		FROM accounts`).Scan(&opening, &balances, &unswept); err != nil {
		t.Fatal(err)
	}
	if opening != balances+unswept {
		t.Fatalf("деньги не сохранились: выдано %s, на счетах %s, комиссий %s",
			money.Amount(opening), money.Amount(balances), money.Amount(unswept))
	}

	applied := map[string]bool{}
	rows, err := e.accPool.Query(ctx, `SELECT transfer_id::text FROM applied_transfers WHERE status = 'applied'`)
	if err != nil {
		t.Fatal(err)
	}
	for rows.Next() {
		var id string
		_ = rows.Scan(&id)
		applied[id] = true
	}
	rows.Close()

	rows, err = e.txPool.Query(ctx, `SELECT id::text, status FROM transactions`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	seen := 0
	for rows.Next() {
		var id, st string
		_ = rows.Scan(&id, &st)
		switch {
		case st == models.StatusPending:
			t.Errorf("запись %s осталась pending", id)
		case st == models.StatusCompleted && !applied[id]:
			t.Errorf("запись %s completed, но деньги не переведены", id)
		case st == models.StatusFailed && applied[id]:
			t.Errorf("запись %s failed, но деньги переведены", id)
		}
		if applied[id] {
			seen++
		}
	}
	if seen != len(applied) {
		t.Errorf("применённых переводов %d, из них с записью в БД транзакций только %d", len(applied), seen)
	}
}

func TestSagaHappyPathAndReplay(t *testing.T) {
	e := newSagaEnv(t)
	ctx := context.Background()

	tx, replay, err := e.svc.Create(ctx, e.transfer("key-1", e.alice, e.bob, money.Rubles(100)))
	if err != nil || replay || tx.Status != models.StatusCompleted {
		t.Fatalf("перевод: %+v replay=%v err=%v", tx, replay, err)
	}
	again, replay, err := e.svc.Create(ctx, e.transfer("key-1", e.alice, e.bob, money.Rubles(100)))
	if err != nil || !replay || again.ID != tx.ID {
		t.Fatalf("повтор с тем же ключом должен вернуть ту же операцию: %+v replay=%v err=%v", again, replay, err)
	}
	if got := e.balance(t, e.alice); got != money.Rubles(1000)-money.Rubles(101) {
		t.Fatalf("баланс отправителя %s", got)
	}
	if got := e.balance(t, e.bob); got != money.Rubles(1100) {
		t.Fatalf("баланс получателя %s", got)
	}

	_, _, err = e.svc.Create(ctx, e.transfer("key-1", e.alice, e.bob, money.Rubles(5)))
	if apperr.Reason(err) != apperr.ReasonIdempotencyConflict {
		t.Fatalf("тот же ключ для другой суммы: ожидался IDEMPOTENCY_CONFLICT, получено %v", err)
	}
	e.assertConsistent(t)
}

// Ответ Account Service потерян: клиент получает «исход неизвестен», повторяет запрос
// с тем же ключом и получает успех, а деньги списаны один раз.
func TestSagaLostResponseReplayCompletes(t *testing.T) {
	e := newSagaEnv(t)
	ctx := context.Background()
	e.faults.inject(dropResponse)

	tx, _, err := e.svc.Create(ctx, e.transfer("key-lost", e.alice, e.bob, money.Rubles(10)))
	if apperr.Reason(err) != apperr.ReasonOutcomeUnknown || status.Code(err) != codes.Unavailable {
		t.Fatalf("ожидался OUTCOME_UNKNOWN, получено %v", err)
	}
	if st := e.status(t, tx.ID); st != models.StatusPending {
		t.Fatalf("запись должна остаться pending, статус %s", st)
	}

	again, replay, err := e.svc.Create(ctx, e.transfer("key-lost", e.alice, e.bob, money.Rubles(10)))
	if err != nil || !replay || again.Status != models.StatusCompleted || again.ID != tx.ID {
		t.Fatalf("повтор должен довести перевод до конца: %+v replay=%v err=%v", again, replay, err)
	}
	if got := e.balance(t, e.bob); got != money.Rubles(1010) {
		t.Fatalf("получатель должен получить деньги ровно один раз, баланс %s", got)
	}
	e.assertConsistent(t)
}

// Ответ потерян, клиент не повторяет: recovery-воркер узнаёт, что перевод применён.
func TestSagaLostResponseRecoveryCompletes(t *testing.T) {
	e := newSagaEnv(t)
	e.faults.inject(dropResponse)

	tx, _, err := e.svc.Create(context.Background(), e.transfer("", e.alice, e.bob, money.Rubles(10)))
	if apperr.Reason(err) != apperr.ReasonOutcomeUnknown {
		t.Fatalf("ожидался OUTCOME_UNKNOWN, получено %v", err)
	}
	e.recoverAll(t)
	if st := e.status(t, tx.ID); st != models.StatusCompleted {
		t.Fatalf("recovery должен пометить применённый перевод completed, статус %s", st)
	}
	e.assertConsistent(t)
}

// Запрос потерян: recovery отменяет перевод, и поздняя доставка того же запроса
// уже не спишет деньги.
func TestSagaLostRequestRecoveryAborts(t *testing.T) {
	e := newSagaEnv(t)
	ctx := context.Background()
	e.faults.inject(dropRequest)

	tx, _, err := e.svc.Create(ctx, e.transfer("", e.alice, e.bob, money.Rubles(10)))
	if apperr.Reason(err) != apperr.ReasonOutcomeUnknown {
		t.Fatalf("ожидался OUTCOME_UNKNOWN, получено %v", err)
	}
	e.recoverAll(t)
	if st := e.status(t, tx.ID); st != models.StatusFailed {
		t.Fatalf("неприменённый перевод должен быть отменён, статус %s", st)
	}

	_, err = e.accounts.ApplyTransfer(ctx, &accountpb.ApplyTransferRequest{
		TransferId: tx.ID, UserId: e.alice.UserID, FromAccountId: e.alice.ID, ToAccountId: e.bob.ID,
		Amount: tx.Amount.Kopecks(), Fee: tx.FeeAmount.Kopecks(),
	})
	if apperr.Reason(err) != apperr.ReasonTransferAborted {
		t.Fatalf("поздний запрос должен быть отклонён с TRANSFER_ABORTED, получено %v", err)
	}
	if got := e.balance(t, e.alice); got != money.Rubles(1000) {
		t.Fatalf("деньги не должны были списаться, баланс %s", got)
	}
	e.assertConsistent(t)
}

// Отказ (нехватка средств, чужой счёт) окончателен и при повторе возвращается тот же.
func TestSagaRejectionsAreFinal(t *testing.T) {
	e := newSagaEnv(t)
	ctx := context.Background()

	_, _, err := e.svc.Create(ctx, e.transfer("big", e.alice, e.bob, money.Rubles(5000)))
	if apperr.Reason(err) != apperr.ReasonInsufficientFunds || status.Code(err) != codes.FailedPrecondition {
		t.Fatalf("ожидался INSUFFICIENT_FUNDS, получено %v", err)
	}
	tx, replay, err := e.svc.Create(ctx, e.transfer("big", e.alice, e.bob, money.Rubles(5000)))
	if !replay || tx.Status != models.StatusFailed || apperr.Reason(err) != apperr.ReasonInsufficientFunds {
		t.Fatalf("повтор отклонённого перевода: %+v replay=%v err=%v", tx, replay, err)
	}

	steal := e.transfer("steal", e.alice, e.bob, money.Rubles(1))
	steal.UserID = e.bob.UserID
	if _, _, err := e.svc.Create(ctx, steal); status.Code(err) != codes.PermissionDenied {
		t.Fatalf("перевод с чужого счёта: ожидался PermissionDenied, получено %v", err)
	}
	history, err := e.svc.History(ctx, e.alice.UserID, "", 50)
	if err != nil {
		t.Fatal(err)
	}
	for _, h := range history {
		if h.UserID != e.alice.UserID && h.Status == models.StatusFailed {
			t.Fatalf("чужая отклонённая попытка видна владельцу счёта: %+v", h)
		}
	}
	e.assertConsistent(t)
}

// Асинхронный перевод: первый вызов теряет ответ, Worker Pool повторяет его,
// повтор идемпотентен — деньги списаны один раз.
func TestSagaAsyncRetryIsIdempotent(t *testing.T) {
	e := newSagaEnv(t)
	e.faults.inject(dropResponse)

	tx, _, err := e.svc.CreateAsync(context.Background(), e.transfer("", e.alice, e.bob, money.Rubles(10)))
	if err != nil || tx.Status != models.StatusPending {
		t.Fatalf("асинхронный перевод: %+v err=%v", tx, err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for e.status(t, tx.ID) == models.StatusPending {
		if time.Now().After(deadline) {
			t.Fatal("асинхронный перевод не завершился за 5 секунд")
		}
		time.Sleep(20 * time.Millisecond)
	}
	if st := e.status(t, tx.ID); st != models.StatusCompleted {
		t.Fatalf("статус %s", st)
	}
	if got := e.balance(t, e.bob); got != money.Rubles(1010) {
		t.Fatalf("получатель должен получить деньги ровно один раз, баланс %s", got)
	}
	e.assertConsistent(t)
}

// Сотни параллельных переводов со случайными сбоями и дублями запросов:
// после recovery обе БД согласованы, деньги сохранились.
func TestSagaUnderFaultsAndConcurrency(t *testing.T) {
	e := newSagaEnv(t)
	ctx := context.Background()
	accounts := []*accmodels.Account{e.alice, e.bob, e.openAccount(t, uuid.NewString()), e.openAccount(t, uuid.NewString())}
	e.faults.random = 0.2

	rng := rand.New(rand.NewSource(1))
	type job struct{ req service.CreateRequest }
	var jobs []job
	for i := 0; i < 400; i++ {
		from, to := accounts[rng.Intn(len(accounts))], accounts[rng.Intn(len(accounts))]
		if from == to {
			continue
		}
		req := e.transfer(uuid.NewString(), from, to, money.Amount(1000+rng.Intn(40000)))
		jobs = append(jobs, job{req})
		if rng.Intn(5) == 0 {
			jobs = append(jobs, job{req}) // клиент повторил запрос параллельно
		}
	}

	var wg sync.WaitGroup
	sem := make(chan struct{}, 32)
	for _, j := range jobs {
		wg.Add(1)
		sem <- struct{}{}
		go func(req service.CreateRequest) {
			defer wg.Done()
			defer func() { <-sem }()
			_, _, err := e.svc.Create(ctx, req)
			if err != nil && !apperr.IsDefinite(err) && apperr.Reason(err) != apperr.ReasonOutcomeUnknown {
				t.Errorf("неожиданная ошибка: %v", err)
			}
		}(j.req)
	}
	wg.Wait()

	e.faults.random = 0
	e.recoverAll(t)
	e.assertConsistent(t)
}
