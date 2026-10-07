package repository_test

// Интеграционные тесты: нужен PostgreSQL (make up && make test-integration).
// Каждый тест работает во временной схеме и не трогает данные стенда.

import (
	"context"
	"errors"
	"math/rand"
	"sync"
	"testing"
	"time"

	"bank_proto_microservice/internal/account/models"
	"bank_proto_microservice/internal/account/repository"
	"bank_proto_microservice/internal/money"
	"bank_proto_microservice/internal/testutil"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

func newRepo(t *testing.T) (*repository.AccountRepository, *pgxpool.Pool) {
	pool := testutil.PostgresSchema(t, "ACCOUNT_TEST_DATABASE_URL", "migrations/account/000001_init_account.up.sql")
	return repository.NewAccountRepository(pool), pool
}

func openAccounts(t *testing.T, repo *repository.AccountRepository, users, perUser int, opening money.Amount) []*models.Account {
	t.Helper()
	var accounts []*models.Account
	for u := 0; u < users; u++ {
		userID := uuid.NewString()
		for i := 0; i < perUser; i++ {
			acc, err := repo.Create(context.Background(), userID, opening, 5)
			if err != nil {
				t.Fatal(err)
			}
			accounts = append(accounts, acc)
		}
	}
	return accounts
}

func balance(t *testing.T, repo *repository.AccountRepository, id string) money.Amount {
	t.Helper()
	acc, err := repo.GetByID(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	return acc.Balance
}

// assertMoneyConserved проверяет главный инвариант: деньги не появляются и не исчезают.
func assertMoneyConserved(t *testing.T, pool *pgxpool.Pool) {
	t.Helper()
	var opening, balances, unswept int64
	err := pool.QueryRow(context.Background(), `
		SELECT SUM(opening_balance), SUM(balance),
		       (SELECT COALESCE(SUM(fee), 0) FROM applied_transfers WHERE status = 'applied' AND NOT fee_swept)
		FROM accounts`).Scan(&opening, &balances, &unswept)
	if err != nil {
		t.Fatal(err)
	}
	if opening != balances+unswept {
		t.Fatalf("деньги не сохранились: выдано %s, на счетах %s, в комиссиях %s",
			money.Amount(opening), money.Amount(balances), money.Amount(unswept))
	}
}

func transfer(from, to *models.Account, userID string, amount money.Amount) models.Transfer {
	return models.Transfer{
		ID: uuid.NewString(), UserID: userID, FromID: from.ID, ToID: to.ID,
		Amount: amount, Fee: money.Fee(amount, 1),
	}
}

type outcome struct {
	applied bool
	err     error
}

// Тысячи параллельных переводов, дубли, встречные переводы и чужие счета:
// деньги сохраняются, каждый transfer_id применяется не больше одного раза,
// взаимоблокировок нет, балансы совпадают с журналом.
func TestApplyTransferConcurrentInvariants(t *testing.T) {
	repo, pool := newRepo(t)
	ctx := context.Background()
	accounts := openAccounts(t, repo, 10, 2, money.Rubles(1000))

	rng := rand.New(rand.NewSource(42))
	var jobs []models.Transfer
	for i := 0; i < 2000; i++ {
		from := accounts[rng.Intn(len(accounts))]
		to := accounts[rng.Intn(len(accounts))]
		if from.ID == to.ID {
			continue
		}
		userID := from.UserID
		if rng.Intn(10) == 0 {
			userID = accounts[rng.Intn(len(accounts))].UserID // иногда чужой счёт
		}
		tr := transfer(from, to, userID, money.Amount(100+rng.Intn(30000)))
		jobs = append(jobs, tr)
		if rng.Intn(10) == 0 {
			jobs = append(jobs, tr) // дубль: тот же transfer_id уйдёт параллельно
		}
	}

	results := make(map[string][]outcome)
	var mu sync.Mutex
	var wg sync.WaitGroup
	queue := make(chan models.Transfer)
	for w := 0; w < 32; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for tr := range queue {
				res, err := repo.ApplyTransfer(ctx, tr)
				mu.Lock()
				results[tr.ID] = append(results[tr.ID], outcome{applied: err == nil && res != nil, err: err})
				mu.Unlock()
			}
		}()
	}
	for _, j := range jobs {
		queue <- j
	}
	close(queue)
	wg.Wait()

	applied := 0
	for id, outs := range results {
		for _, o := range outs {
			if o.err != nil && !errors.Is(o.err, repository.ErrInsufficientFunds) && !errors.Is(o.err, repository.ErrForbidden) {
				t.Fatalf("transfer %s: неожиданная ошибка %v", id, o.err)
			}
		}
		if len(outs) == 2 && (outs[0].applied != outs[1].applied || !errors.Is(outs[0].err, outs[1].err) && !errors.Is(outs[1].err, outs[0].err)) {
			t.Fatalf("дубль transfer %s получил разные исходы: %+v", id, outs)
		}
		if outs[0].applied {
			applied++
		}
	}
	t.Logf("переводов: %d, применено: %d", len(results), applied)
	if applied == 0 || applied == len(results) {
		t.Fatalf("тест не проверил оба исхода: применено %d из %d", applied, len(results))
	}

	var journalApplied int
	if err := pool.QueryRow(ctx, `SELECT COUNT(*) FROM applied_transfers WHERE status = 'applied'`).Scan(&journalApplied); err != nil {
		t.Fatal(err)
	}
	if journalApplied != applied {
		t.Fatalf("в журнале %d применённых переводов, по ответам — %d", journalApplied, applied)
	}

	// Баланс каждого счёта равен начальному минус списания плюс зачисления по журналу.
	rows, err := pool.Query(ctx, `
		SELECT a.id, a.balance,
		       a.opening_balance
		         - COALESCE((SELECT SUM(amount + fee) FROM applied_transfers WHERE status = 'applied' AND from_account_id = a.id), 0)
		         + COALESCE((SELECT SUM(amount) FROM applied_transfers WHERE status = 'applied' AND to_account_id = a.id), 0)
		FROM accounts a WHERE a.id <> $1`, repository.SystemBankAccountID)
	if err != nil {
		t.Fatal(err)
	}
	for rows.Next() {
		var id string
		var actual, expected int64
		if err := rows.Scan(&id, &actual, &expected); err != nil {
			t.Fatal(err)
		}
		if actual != expected {
			t.Errorf("счёт %s: баланс %s, по журналу %s", id, money.Amount(actual), money.Amount(expected))
		}
	}
	rows.Close()
	assertMoneyConserved(t, pool)

	// Перенос комиссий на системный счёт тоже сохраняет деньги.
	var fees int64
	if err := pool.QueryRow(ctx, `SELECT COALESCE(SUM(fee), 0) FROM applied_transfers WHERE status = 'applied'`).Scan(&fees); err != nil {
		t.Fatal(err)
	}
	for {
		n, _, err := repo.SweepFees(ctx, 100)
		if err != nil {
			t.Fatal(err)
		}
		if n == 0 {
			break
		}
	}
	if got := balance(t, repo, repository.SystemBankAccountID); got.Kopecks() != fees {
		t.Fatalf("на системном счёте %s, комиссий собрано %s", got, money.Amount(fees))
	}
	assertMoneyConserved(t, pool)
}

// Встречные переводы A->B и B->A не приводят к взаимоблокировке.
func TestOpposingTransfersDoNotDeadlock(t *testing.T) {
	repo, pool := newRepo(t)
	accounts := openAccounts(t, repo, 2, 1, money.Rubles(100000))
	a, b := accounts[0], accounts[1]
	deadlocksBefore := deadlocks(t, pool)

	// При неправильном порядке блокировок каждая взаимоблокировка стоит секунду
	// (deadlock_timeout), поэтому тест ограничен по времени, а не висит минутами.
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	var wg sync.WaitGroup
	errs := make(chan error, 400)
	for i := 0; i < 200; i++ {
		for _, pair := range [][2]*models.Account{{a, b}, {b, a}} {
			wg.Add(1)
			go func(from, to *models.Account) {
				defer wg.Done()
				_, err := repo.ApplyTransfer(ctx, transfer(from, to, from.UserID, money.Rubles(1)))
				errs <- err
			}(pair[0], pair[1])
		}
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatalf("встречный перевод завершился ошибкой: %v", err)
		}
	}
	if n := deadlocks(t, pool) - deadlocksBefore; n != 0 {
		t.Fatalf("PostgreSQL обнаружил %d взаимоблокировок", n)
	}
	assertMoneyConserved(t, pool)
}

func deadlocks(t *testing.T, pool *pgxpool.Pool) int64 {
	t.Helper()
	var n int64
	if err := pool.QueryRow(context.Background(),
		`SELECT deadlocks FROM pg_stat_database WHERE datname = current_database()`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func TestApplyTransferIsIdempotent(t *testing.T) {
	repo, pool := newRepo(t)
	ctx := context.Background()
	accounts := openAccounts(t, repo, 2, 1, money.Rubles(1000))
	a, b := accounts[0], accounts[1]
	tr := transfer(a, b, a.UserID, money.Rubles(100))

	res, err := repo.ApplyTransfer(ctx, tr)
	if err != nil || res.AlreadyApplied {
		t.Fatalf("первый вызов: %+v, %v", res, err)
	}
	res, err = repo.ApplyTransfer(ctx, tr)
	if err != nil || !res.AlreadyApplied {
		t.Fatalf("повтор должен вернуть already_applied: %+v, %v", res, err)
	}
	if got := balance(t, repo, a.ID); got != money.Rubles(1000)-money.Rubles(101) {
		t.Fatalf("деньги списаны не один раз: баланс %s", got)
	}

	changed := tr
	changed.Amount = money.Rubles(50)
	if _, err := repo.ApplyTransfer(ctx, changed); !errors.Is(err, repository.ErrTransferMismatch) {
		t.Fatalf("тот же transfer_id с другой суммой: ожидался ErrTransferMismatch, получено %v", err)
	}
	assertMoneyConserved(t, pool)
}

// Отказ окончателен: тот же transfer_id не применится, даже когда денег станет достаточно.
func TestRejectionIsFinal(t *testing.T) {
	repo, pool := newRepo(t)
	ctx := context.Background()
	accounts := openAccounts(t, repo, 3, 1, money.Rubles(100))
	a, b, c := accounts[0], accounts[1], accounts[2]

	big := transfer(a, b, a.UserID, money.Rubles(150))
	if _, err := repo.ApplyTransfer(ctx, big); !errors.Is(err, repository.ErrInsufficientFunds) {
		t.Fatalf("ожидался отказ из-за нехватки средств, получено %v", err)
	}
	if _, err := repo.ApplyTransfer(ctx, transfer(c, a, c.UserID, money.Rubles(90))); err != nil {
		t.Fatal(err)
	}
	if _, err := repo.ApplyTransfer(ctx, big); !errors.Is(err, repository.ErrInsufficientFunds) {
		t.Fatalf("повтор отклонённого перевода должен вернуть тот же отказ, получено %v", err)
	}
	if got := balance(t, repo, b.ID); got != money.Rubles(100) {
		t.Fatalf("отклонённый перевод всё-таки зачислен: баланс получателя %s", got)
	}

	foreign := transfer(a, b, b.UserID, money.Rubles(1))
	if _, err := repo.ApplyTransfer(ctx, foreign); !errors.Is(err, repository.ErrForbidden) {
		t.Fatalf("перевод с чужого счёта: ожидался ErrForbidden, получено %v", err)
	}
	assertMoneyConserved(t, pool)
}

// ResolveTransfer навсегда отменяет неприменённый перевод: поздний ApplyTransfer отклоняется.
func TestResolveFencesLateApply(t *testing.T) {
	repo, pool := newRepo(t)
	ctx := context.Background()
	accounts := openAccounts(t, repo, 2, 1, money.Rubles(1000))
	a, b := accounts[0], accounts[1]

	lost := transfer(a, b, a.UserID, money.Rubles(10))
	applied, err := repo.ResolveTransfer(ctx, lost.ID)
	if err != nil || applied {
		t.Fatalf("неприменённый перевод должен быть отменён: applied=%v err=%v", applied, err)
	}
	if _, err := repo.ApplyTransfer(ctx, lost); !errors.Is(err, repository.ErrTransferAborted) {
		t.Fatalf("поздний ApplyTransfer должен быть отклонён, получено %v", err)
	}

	done := transfer(a, b, a.UserID, money.Rubles(10))
	if _, err := repo.ApplyTransfer(ctx, done); err != nil {
		t.Fatal(err)
	}
	if applied, err := repo.ResolveTransfer(ctx, done.ID); err != nil || !applied {
		t.Fatalf("применённый перевод должен остаться применённым: applied=%v err=%v", applied, err)
	}
	if got := balance(t, repo, a.ID); got != money.Rubles(1000)-money.Amount(1010) {
		t.Fatalf("баланс %s", got)
	}
	assertMoneyConserved(t, pool)
}

func TestClosedAccountCannotSendOrReceive(t *testing.T) {
	repo, pool := newRepo(t)
	ctx := context.Background()
	accounts := openAccounts(t, repo, 2, 1, money.Rubles(1000))
	a, b := accounts[0], accounts[1]

	if err := repo.Close(ctx, b.ID, b.UserID); err != nil {
		t.Fatal(err)
	}
	if err := repo.Close(ctx, b.ID, b.UserID); !errors.Is(err, repository.ErrAlreadyClosed) {
		t.Fatalf("повторное закрытие: %v", err)
	}
	if err := repo.Close(ctx, a.ID, b.UserID); !errors.Is(err, repository.ErrForbidden) {
		t.Fatalf("закрытие чужого счёта: %v", err)
	}
	if _, err := repo.ApplyTransfer(ctx, transfer(a, b, a.UserID, money.Rubles(1))); !errors.Is(err, repository.ErrRecipientClosed) {
		t.Fatalf("перевод на закрытый счёт: %v", err)
	}
	if _, err := repo.ApplyTransfer(ctx, transfer(b, a, b.UserID, money.Rubles(1))); !errors.Is(err, repository.ErrAccountClosed) {
		t.Fatalf("перевод с закрытого счёта: %v", err)
	}
	assertMoneyConserved(t, pool)
}

// Лимит в 5 активных счетов соблюдается и при параллельных запросах.
func TestAccountLimitUnderConcurrency(t *testing.T) {
	repo, _ := newRepo(t)
	userID := uuid.NewString()

	var wg sync.WaitGroup
	var mu sync.Mutex
	created, limited := 0, 0
	for i := 0; i < 12; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := repo.Create(context.Background(), userID, money.Rubles(1), 5)
			mu.Lock()
			defer mu.Unlock()
			switch {
			case err == nil:
				created++
			case errors.Is(err, repository.ErrAccountLimitReached):
				limited++
			default:
				t.Errorf("неожиданная ошибка: %v", err)
			}
		}()
	}
	wg.Wait()
	if created != 5 || limited != 7 {
		t.Fatalf("открыто %d счетов (ожидалось 5), отказов %d (ожидалось 7)", created, limited)
	}
}
