package repository

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"math/big"
	"time"

	"bank_proto_microservice/internal/account/models"
	"bank_proto_microservice/internal/apperr"
	"bank_proto_microservice/internal/money"
	"bank_proto_microservice/internal/utils"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// SystemBankAccountID — системный счёт банка, куда переносятся комиссии.
const SystemBankAccountID = "00000000-0000-0000-0000-000000000001"

var (
	ErrAccountNotFound     = errors.New("счёт не найден")
	ErrForbidden           = errors.New("нет доступа к данному счёту")
	ErrAccountClosed       = errors.New("счёт списания закрыт")
	ErrAlreadyClosed       = errors.New("счёт уже закрыт")
	ErrAccountLimitReached = errors.New("достигнут лимит активных счетов")
	ErrRecipientNotFound   = errors.New("счёт получателя не найден")
	ErrRecipientClosed     = errors.New("счёт получателя закрыт")
	ErrInsufficientFunds   = errors.New("недостаточно средств")
	ErrTransferAborted     = errors.New("перевод отменён: его исход не был подтверждён вовремя")
	ErrTransferMismatch    = errors.New("transfer_id уже использован с другими параметрами")
	ErrConcurrency         = errors.New("конфликт параллельных операций со счётом, повторите запрос")
)

// Отказы, которые записываются в журнал как окончательный исход перевода.
var rejectionReasons = map[error]string{
	ErrAccountNotFound:   apperr.ReasonAccountNotFound,
	ErrForbidden:         apperr.ReasonAccountForbidden,
	ErrAccountClosed:     apperr.ReasonAccountClosed,
	ErrRecipientNotFound: apperr.ReasonRecipientNotFound,
	ErrRecipientClosed:   apperr.ReasonRecipientClosed,
	ErrInsufficientFunds: apperr.ReasonInsufficientFunds,
	ErrConcurrency:       apperr.ReasonConcurrencyConflict,
	ErrTransferAborted:   apperr.ReasonTransferAborted,
}

func rejectionReason(err error) (string, bool) {
	for sentinel, reason := range rejectionReasons {
		if errors.Is(err, sentinel) {
			return reason, true
		}
	}
	return "", false
}

func rejectionError(reason string) error {
	for sentinel, r := range rejectionReasons {
		if r == reason {
			return sentinel
		}
	}
	return ErrTransferAborted
}

type AccountRepository struct {
	db *pgxpool.Pool
}

func NewAccountRepository(db *pgxpool.Pool) *AccountRepository {
	return &AccountRepository{db: db}
}

const accountColumns = "id, user_id, balance, status, created_at"

func scanAccount(row pgx.Row) (*models.Account, error) {
	var a models.Account
	var balance int64
	if err := row.Scan(&a.ID, &a.UserID, &balance, &a.Status, &a.CreatedAt); err != nil {
		return nil, err
	}
	a.Balance = money.Amount(balance)
	return &a, nil
}

func newAccountNumber() (string, error) {
	n, err := rand.Int(rand.Reader, big.NewInt(1_000_000_000_000))
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("13%012d", n.Int64()), nil
}

// Create открывает счёт с начальным балансом opening. Лимит активных счетов проверяется
// под advisory-блокировкой пользователя: без неё два параллельных запроса оба увидели бы
// «4 счёта» и открыли бы шестой.
func (r *AccountRepository) Create(ctx context.Context, userID string, opening money.Amount, maxActive int) (*models.Account, error) {
	tx, err := r.db.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(context.WithoutCancel(ctx))

	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1, 0))`, userID); err != nil {
		return nil, err
	}
	var active int
	if err := tx.QueryRow(ctx, `SELECT COUNT(*) FROM accounts WHERE user_id = $1 AND status = 'active'`, userID).Scan(&active); err != nil {
		return nil, err
	}
	if active >= maxActive {
		return nil, ErrAccountLimitReached
	}

	for attempt := 0; attempt < 10; attempt++ {
		id, err := newAccountNumber()
		if err != nil {
			return nil, err
		}
		acc, err := scanAccount(tx.QueryRow(ctx, `
			INSERT INTO accounts (id, user_id, balance, opening_balance, status)
			VALUES ($1, $2, $3, $3, 'active')
			ON CONFLICT (id) DO NOTHING
			RETURNING `+accountColumns, id, userID, opening.Kopecks()))
		if errors.Is(err, pgx.ErrNoRows) {
			continue // номер уже занят — генерируем новый
		}
		if err != nil {
			return nil, err
		}
		return acc, tx.Commit(ctx)
	}
	return nil, errors.New("не удалось сгенерировать уникальный номер счёта")
}

func (r *AccountRepository) GetByID(ctx context.Context, accountID string) (*models.Account, error) {
	acc, err := scanAccount(r.db.QueryRow(ctx, `SELECT `+accountColumns+` FROM accounts WHERE id = $1`, accountID))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrAccountNotFound
	}
	return acc, err
}

func (r *AccountRepository) ListByUser(ctx context.Context, userID string) ([]models.Account, error) {
	rows, err := r.db.Query(ctx, `SELECT `+accountColumns+` FROM accounts WHERE user_id = $1 ORDER BY created_at DESC`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	accounts := []models.Account{}
	for rows.Next() {
		a, err := scanAccount(rows)
		if err != nil {
			return nil, err
		}
		accounts = append(accounts, *a)
	}
	return accounts, rows.Err()
}

// Close закрывает счёт владельца. Все условия проверяются в одном UPDATE,
// поэтому закрытие атомарно относительно параллельных переводов (они блокируют строку).
func (r *AccountRepository) Close(ctx context.Context, accountID, userID string) error {
	tag, err := r.db.Exec(ctx,
		`UPDATE accounts SET status = 'closed' WHERE id = $1 AND user_id = $2 AND status = 'active'`,
		accountID, userID)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 1 {
		return nil
	}
	acc, err := r.GetByID(ctx, accountID)
	if err != nil {
		return err
	}
	if acc.UserID != userID {
		return ErrForbidden
	}
	return ErrAlreadyClosed
}

// ApplyTransfer выполняет шаг саги: списывает Amount+Fee со счёта From и зачисляет Amount на To.
//
// Гарантия: у каждого transfer_id ровно один окончательный исход — applied или aborted,
// и он записан в applied_transfers. Поэтому повторы, параллельные дубли и recovery-воркер
// с тем же transfer_id никогда не спишут деньги второй раз и не применят отклонённый перевод.
func (r *AccountRepository) ApplyTransfer(ctx context.Context, t models.Transfer) (*models.ApplyResult, error) {
	res, err := r.applyWithRetry(ctx, t)
	if err == nil {
		return res, nil
	}
	reason, isRejection := rejectionReason(err)
	if !isRejection {
		return nil, err // исход неизвестен вызывающему (таймаут, сбой БД) — его уточнит ResolveTransfer
	}

	// Отказ фиксируется навсегда, иначе параллельная попытка с тем же transfer_id
	// (повтор клиента, ретрай воркера) могла бы применить перевод уже после отказа.
	applied, storedReason, err := r.finalize(ctx, t.ID, reason)
	if err != nil {
		return nil, err
	}
	if applied {
		// Параллельная попытка с тем же transfer_id успела применить перевод.
		return &models.ApplyResult{AlreadyApplied: true}, nil
	}
	return nil, rejectionError(storedReason)
}

// ResolveTransfer определяет окончательный исход перевода: если он ещё не применён,
// transfer_id помечается отменённым и применить его после этого уже нельзя.
func (r *AccountRepository) ResolveTransfer(ctx context.Context, transferID string) (applied bool, err error) {
	applied, _, err = r.finalize(ctx, transferID, apperr.ReasonTransferAborted)
	return applied, err
}

// finalize записывает для transfer_id исход aborted, если исход ещё не записан,
// и возвращает окончательный исход. Если перевод с этим id сейчас выполняется,
// INSERT ждёт его завершения на уникальном индексе.
func (r *AccountRepository) finalize(ctx context.Context, transferID, reason string) (applied bool, storedReason string, err error) {
	if _, err = r.db.Exec(ctx, `
		INSERT INTO applied_transfers (transfer_id, status, reason)
		VALUES ($1, 'aborted', $2)
		ON CONFLICT (transfer_id) DO NOTHING`, transferID, reason); err != nil {
		return false, "", err
	}
	var status string
	var stored *string
	if err = r.db.QueryRow(ctx,
		`SELECT status, reason FROM applied_transfers WHERE transfer_id = $1`, transferID,
	).Scan(&status, &stored); err != nil {
		return false, "", err
	}
	if stored != nil {
		storedReason = *stored
	}
	return status == "applied", storedReason, nil
}

// applyWithRetry повторяет транзакцию при deadlock/serialization failure — такие ошибки
// PostgreSQL означают, что транзакция откатилась целиком и её можно безопасно повторить.
func (r *AccountRepository) applyWithRetry(ctx context.Context, t models.Transfer) (*models.ApplyResult, error) {
	const attempts = 3
	for i := 1; ; i++ {
		res, err := r.applyOnce(ctx, t)
		code := utils.PgCode(err)
		retryable := code == utils.PgDeadlockDetected || code == utils.PgSerializationFailed
		if !retryable || i == attempts {
			return res, classify(err)
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(time.Duration(i*5) * time.Millisecond):
		}
	}
}

// classify превращает ошибки PostgreSQL, после которых транзакция точно откатилась, в ErrConcurrency.
func classify(err error) error {
	switch utils.PgCode(err) {
	case utils.PgDeadlockDetected, utils.PgSerializationFailed, utils.PgLockNotAvailable:
		return fmt.Errorf("%w (%v)", ErrConcurrency, err)
	case utils.PgQueryCanceled:
		if !utils.IsCanceled(err) { // statement_timeout, а не дедлайн запроса
			return fmt.Errorf("%w (%v)", ErrConcurrency, err)
		}
	}
	return err
}

type lockedAccount struct {
	userID  string
	balance money.Amount
	status  string
}

func (r *AccountRepository) applyOnce(ctx context.Context, t models.Transfer) (*models.ApplyResult, error) {
	tx, err := r.db.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(context.WithoutCancel(ctx))

	// 1. Занимаем transfer_id. Параллельный дубль с тем же id будет ждать на уникальном
	//    индексе, пока эта транзакция не завершится, — дубли исполняются строго по очереди.
	tag, err := tx.Exec(ctx, `
		INSERT INTO applied_transfers (transfer_id, status, from_account_id, to_account_id, amount, fee)
		VALUES ($1, 'applied', $2, $3, $4, $5)
		ON CONFLICT (transfer_id) DO NOTHING`,
		t.ID, t.FromID, t.ToID, t.Amount.Kopecks(), t.Fee.Kopecks())
	if err != nil {
		return nil, err
	}
	if tag.RowsAffected() == 0 {
		return existingOutcome(ctx, tx, t)
	}

	// 2. Блокируем оба счёта в порядке возрастания id. Одинаковый порядок у всех транзакций
	//    исключает взаимоблокировку встречных переводов A->B и B->A.
	rows, err := tx.Query(ctx,
		`SELECT id, user_id, balance, status FROM accounts WHERE id = ANY($1) ORDER BY id FOR UPDATE`,
		[]string{t.FromID, t.ToID})
	if err != nil {
		return nil, err
	}
	locked := make(map[string]lockedAccount, 2)
	for rows.Next() {
		var id string
		var a lockedAccount
		var balance int64
		if err := rows.Scan(&id, &a.userID, &balance, &a.status); err != nil {
			rows.Close()
			return nil, err
		}
		a.balance = money.Amount(balance)
		locked[id] = a
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}

	// 3. Проверки выполняются по заблокированным строкам — между проверкой и списанием
	//    никто не может изменить баланс или статус.
	from, fromOK := locked[t.FromID]
	to, toOK := locked[t.ToID]
	switch {
	case !fromOK:
		return nil, ErrAccountNotFound
	case from.userID != t.UserID:
		return nil, ErrForbidden
	case from.status != models.StatusActive:
		return nil, ErrAccountClosed
	case !toOK:
		return nil, ErrRecipientNotFound
	case to.status != models.StatusActive:
		return nil, ErrRecipientClosed
	case from.balance < t.Amount+t.Fee:
		return nil, ErrInsufficientFunds
	}

	// 4. Движение денег. Комиссия остаётся в applied_transfers и позже переносится
	//    на системный счёт (SweepFees), поэтому системный счёт здесь не блокируется.
	if _, err := tx.Exec(ctx, `UPDATE accounts SET balance = balance - $1 WHERE id = $2`,
		(t.Amount + t.Fee).Kopecks(), t.FromID); err != nil {
		return nil, err
	}
	if _, err := tx.Exec(ctx, `UPDATE accounts SET balance = balance + $1 WHERE id = $2`,
		t.Amount.Kopecks(), t.ToID); err != nil {
		return nil, err
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}
	return &models.ApplyResult{FromUserID: from.userID, ToUserID: to.userID}, nil
}

// existingOutcome возвращает записанный ранее исход перевода.
func existingOutcome(ctx context.Context, tx pgx.Tx, t models.Transfer) (*models.ApplyResult, error) {
	var status string
	var reason, from, to *string
	var amount, fee int64
	err := tx.QueryRow(ctx, `
		SELECT status, reason, from_account_id, to_account_id, amount, fee
		FROM applied_transfers WHERE transfer_id = $1`, t.ID,
	).Scan(&status, &reason, &from, &to, &amount, &fee)
	if err != nil {
		return nil, err
	}
	if status != "applied" {
		stored := ""
		if reason != nil {
			stored = *reason
		}
		return nil, rejectionError(stored)
	}
	if deref(from) != t.FromID || deref(to) != t.ToID || amount != t.Amount.Kopecks() || fee != t.Fee.Kopecks() {
		return nil, ErrTransferMismatch
	}
	return &models.ApplyResult{AlreadyApplied: true}, nil
}

func deref(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

// SweepFees переносит накопленные комиссии (не более batch записей) на системный счёт
// в одной транзакции. Системный счёт обновляется раз в несколько секунд, а не каждым
// переводом, — так он перестаёт быть общей «горячей» строкой для всех переводов.
func (r *AccountRepository) SweepFees(ctx context.Context, batch int) (count int64, total money.Amount, err error) {
	tx, err := r.db.Begin(ctx)
	if err != nil {
		return 0, 0, err
	}
	defer tx.Rollback(context.WithoutCancel(ctx))

	var sum, credited int64
	err = tx.QueryRow(ctx, `
		WITH batch AS (
			SELECT transfer_id FROM applied_transfers
			WHERE status = 'applied' AND fee > 0 AND NOT fee_swept
			ORDER BY created_at
			LIMIT $2
			FOR UPDATE SKIP LOCKED
		), swept AS (
			UPDATE applied_transfers t SET fee_swept = TRUE
			FROM batch WHERE t.transfer_id = batch.transfer_id
			RETURNING t.fee
		), credited AS (
			UPDATE accounts SET balance = balance + (SELECT COALESCE(SUM(fee), 0) FROM swept)
			WHERE id = $1 AND EXISTS (SELECT 1 FROM swept)
			RETURNING 1
		)
		SELECT (SELECT COUNT(*) FROM swept), (SELECT COALESCE(SUM(fee), 0) FROM swept), (SELECT COUNT(*) FROM credited)`,
		SystemBankAccountID, batch,
	).Scan(&count, &sum, &credited)
	if err != nil {
		return 0, 0, err
	}
	if count > 0 && credited != 1 {
		return 0, 0, errors.New("системный счёт банка не найден: комиссии не перенесены")
	}
	return count, money.Amount(sum), tx.Commit(ctx)
}
