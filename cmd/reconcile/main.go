// reconcile — сверка двух баз данных банка (запускается после нагрузочного теста
// или эксперимента со сбоями):
//
//  1. Сохранность денег в БД счетов: Σ opening_balance = Σ balance + Σ несписанных комиссий.
//  2. Согласованность саги между БД счетов и БД транзакций:
//     completed ⇔ перевод применён с теми же суммами; failed ⇒ деньги не двигались;
//     нет применённых переводов без записи в БД транзакций.
//
// Обе таблицы читаются потоково в порядке UUID (merge join), память не зависит от объёма.
// Код выхода 1 — найдены нарушения.
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"sort"
	"time"

	"bank_proto_microservice/internal/money"
	"bank_proto_microservice/internal/utils"

	"github.com/jackc/pgx/v5"
)

type accRow struct {
	id, status, reason, from, to string
	amount, fee                  int64
}

type txRow struct {
	id, status, reason, from, to string
	amount, fee                  int64
	createdAt                    time.Time
}

type report struct {
	completed, failed, pending int
	completedAmount, feesTotal money.Amount
	failedByReason             map[string]int
	stalePending               int
	violations                 map[string]int
	examples                   map[string]string
}

func (r *report) violation(kind, id string) {
	r.violations[kind]++
	if _, ok := r.examples[kind]; !ok {
		r.examples[kind] = id
	}
}

func main() {
	accURL := flag.String("accounts", utils.Env("ACCOUNT_DATABASE_URL",
		"postgres://account_user:account_password@localhost:5432/account_db?sslmode=disable"), "БД счетов")
	txURL := flag.String("transactions", utils.Env("TX_DATABASE_URL",
		"postgres://tx_user:tx_password@localhost:5433/tx_db?sslmode=disable"), "БД транзакций")
	grace := flag.Duration("pending-grace", time.Minute,
		"pending-записи моложе этого возраста не считаются зависшими (их ещё доводит recovery)")
	flag.Parse()

	ctx := context.Background()
	acc, err := pgx.Connect(ctx, *accURL)
	if err != nil {
		fail("подключение к БД счетов: %v", err)
	}
	defer acc.Close(ctx)
	txdb, err := pgx.Connect(ctx, *txURL)
	if err != nil {
		fail("подключение к БД транзакций: %v", err)
	}
	defer txdb.Close(ctx)

	ok := checkMoney(ctx, acc)
	rep, err := checkSaga(ctx, acc, txdb, *grace)
	if err != nil {
		fail("сверка саги: %v", err)
	}
	printSaga(rep)
	if !ok || len(rep.violations) > 0 {
		fmt.Println("\nРЕЗУЛЬТАТ: НАЙДЕНЫ НАРУШЕНИЯ")
		os.Exit(1)
	}
	fmt.Println("\nРЕЗУЛЬТАТ: ОК, расхождений нет")
}

func fail(format string, args ...any) {
	fmt.Fprintf(os.Stderr, format+"\n", args...)
	os.Exit(2)
}

func checkMoney(ctx context.Context, acc *pgx.Conn) bool {
	var accounts int
	var opening, balances, unswept, systemBalance int64
	err := acc.QueryRow(ctx, `
		SELECT COUNT(*), COALESCE(SUM(opening_balance), 0), COALESCE(SUM(balance), 0),
		       (SELECT COALESCE(SUM(fee), 0) FROM applied_transfers WHERE status = 'applied' AND NOT fee_swept),
		       (SELECT balance FROM accounts WHERE id = '00000000-0000-0000-0000-000000000001')
		FROM accounts`).Scan(&accounts, &opening, &balances, &unswept, &systemBalance)
	if err != nil {
		fail("проверка сохранности денег: %v", err)
	}
	diff := opening - balances - unswept
	fmt.Println("== Сохранность денег (БД счетов) ==")
	fmt.Printf("  счетов:                         %d\n", accounts)
	fmt.Printf("  выдано при открытии счетов:     %s ₽\n", money.Amount(opening))
	fmt.Printf("  сумма балансов:                 %s ₽ (из них системный счёт: %s ₽)\n", money.Amount(balances), money.Amount(systemBalance))
	fmt.Printf("  комиссии, ещё не перенесённые:  %s ₽\n", money.Amount(unswept))
	if diff != 0 {
		fmt.Printf("  НАРУШЕНИЕ: расхождение %s ₽\n", money.Amount(diff))
		return false
	}
	fmt.Println("  ОК: деньги не появились и не исчезли")
	return true
}

func checkSaga(ctx context.Context, acc, txdb *pgx.Conn, grace time.Duration) (*report, error) {
	rep := &report{failedByReason: map[string]int{}, violations: map[string]int{}, examples: map[string]string{}}

	accRows, err := acc.Query(ctx, `
		SELECT transfer_id::text, status, COALESCE(reason, ''), COALESCE(from_account_id, ''),
		       COALESCE(to_account_id, ''), amount, fee
		FROM applied_transfers ORDER BY transfer_id`)
	if err != nil {
		return nil, err
	}
	defer accRows.Close()
	txRows, err := txdb.Query(ctx, `
		SELECT id::text, status, COALESCE(failure_reason, ''), from_account_id, to_account_id,
		       amount, fee_amount, created_at
		FROM transactions ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer txRows.Close()

	nextAcc := func() (*accRow, error) {
		if !accRows.Next() {
			return nil, accRows.Err()
		}
		var r accRow
		return &r, accRows.Scan(&r.id, &r.status, &r.reason, &r.from, &r.to, &r.amount, &r.fee)
	}
	nextTx := func() (*txRow, error) {
		if !txRows.Next() {
			return nil, txRows.Err()
		}
		var r txRow
		return &r, txRows.Scan(&r.id, &r.status, &r.reason, &r.from, &r.to, &r.amount, &r.fee, &r.createdAt)
	}

	a, err := nextAcc()
	if err != nil {
		return nil, err
	}
	t, err := nextTx()
	if err != nil {
		return nil, err
	}
	now := time.Now()
	for a != nil || t != nil {
		switch {
		case t == nil || (a != nil && a.id < t.id):
			// Запись есть только в журнале Account Service.
			if a.status == "applied" {
				rep.violation("деньги переведены, но записи о переводе нет", a.id)
			}
			if a, err = nextAcc(); err != nil {
				return nil, err
			}
			continue
		case a == nil || t.id < a.id:
			rep.compare(t, nil, now, grace)
		default:
			rep.compare(t, a, now, grace)
			if a, err = nextAcc(); err != nil {
				return nil, err
			}
		}
		if t, err = nextTx(); err != nil {
			return nil, err
		}
	}
	return rep, nil
}

func (r *report) compare(t *txRow, a *accRow, now time.Time, grace time.Duration) {
	applied := a != nil && a.status == "applied"
	switch t.status {
	case "completed":
		r.completed++
		r.completedAmount += money.Amount(t.amount)
		r.feesTotal += money.Amount(t.fee)
		switch {
		case !applied:
			r.violation("статус completed, но деньги не переведены", t.id)
		case a.from != t.from || a.to != t.to || a.amount != t.amount || a.fee != t.fee:
			r.violation("суммы или счета в двух БД не совпадают", t.id)
		}
	case "failed":
		r.failed++
		r.failedByReason[t.reason]++
		if applied {
			r.violation("статус failed, но деньги переведены", t.id)
		}
	default:
		r.pending++
		if now.Sub(t.createdAt) > grace {
			r.stalePending++
		}
	}
}

func printSaga(r *report) {
	fmt.Println("\n== Согласованность саги (БД транзакций ↔ БД счетов) ==")
	fmt.Printf("  completed: %d (сумма %s ₽, комиссии %s ₽)\n", r.completed, r.completedAmount, r.feesTotal)
	fmt.Printf("  failed:    %d\n", r.failed)
	reasons := make([]string, 0, len(r.failedByReason))
	for reason := range r.failedByReason {
		reasons = append(reasons, reason)
	}
	sort.Slice(reasons, func(i, j int) bool { return r.failedByReason[reasons[i]] > r.failedByReason[reasons[j]] })
	for _, reason := range reasons {
		fmt.Printf("    %-24s %d\n", reason, r.failedByReason[reason])
	}
	fmt.Printf("  pending:   %d", r.pending)
	if r.stalePending > 0 {
		fmt.Printf(" (из них %d старше порога — recovery-воркер не работает или Account Service недоступен)", r.stalePending)
	}
	fmt.Println()

	if len(r.violations) == 0 {
		fmt.Println("  ОК: исходы в двух БД согласованы")
		return
	}
	for kind, n := range r.violations {
		fmt.Printf("  НАРУШЕНИЕ: %s — %d (пример: %s)\n", kind, n, r.examples[kind])
	}
}
