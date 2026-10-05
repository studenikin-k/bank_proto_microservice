package repository

import (
	"context"
	"errors"
	"time"

	"bank_proto_microservice/internal/money"
	"bank_proto_microservice/internal/transaction/models"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

var ErrNotFound = errors.New("транзакция не найдена")

type TransactionRepository struct {
	db *pgxpool.Pool
}

func NewTransactionRepository(db *pgxpool.Pool) *TransactionRepository {
	return &TransactionRepository{db: db}
}

const columns = `id, user_id, idempotency_key, type, from_account_id, to_account_id,
	amount, fee_percent, fee_amount, total_debit, fee_account_id,
	status, COALESCE(failure_reason, ''), COALESCE(failure_message, ''), created_at, updated_at`

func scan(row pgx.Row) (*models.Transaction, error) {
	var t models.Transaction
	var amount, fee, total int64
	err := row.Scan(&t.ID, &t.UserID, &t.IdempotencyKey, &t.Type, &t.FromAccountID, &t.ToAccountID,
		&amount, &t.FeePercent, &fee, &total, &t.FeeAccountID,
		&t.Status, &t.FailureReason, &t.FailureMessage, &t.CreatedAt, &t.UpdatedAt)
	if err != nil {
		return nil, err
	}
	t.Amount, t.FeeAmount, t.TotalDebit = money.Amount(amount), money.Amount(fee), money.Amount(total)
	return &t, nil
}

func scanAll(rows pgx.Rows) ([]models.Transaction, error) {
	defer rows.Close()
	list := []models.Transaction{}
	for rows.Next() {
		t, err := scan(rows)
		if err != nil {
			return nil, err
		}
		list = append(list, *t)
	}
	return list, rows.Err()
}

// CreatePending создаёт запись в статусе pending. Если у пользователя уже есть запись
// с тем же ключом идемпотентности, возвращает её и created = false.
func (r *TransactionRepository) CreatePending(ctx context.Context, t *models.Transaction) (existing *models.Transaction, created bool, err error) {
	err = r.db.QueryRow(ctx, `
		INSERT INTO transactions (id, user_id, idempotency_key, type, from_account_id, to_account_id,
		                          amount, fee_percent, fee_amount, total_debit, fee_account_id, status)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, 'pending')
		ON CONFLICT (user_id, idempotency_key) DO NOTHING
		RETURNING status, created_at, updated_at`,
		t.ID, t.UserID, t.IdempotencyKey, t.Type, t.FromAccountID, t.ToAccountID,
		t.Amount.Kopecks(), t.FeePercent, t.FeeAmount.Kopecks(), t.TotalDebit.Kopecks(), t.FeeAccountID,
	).Scan(&t.Status, &t.CreatedAt, &t.UpdatedAt)
	if err == nil {
		return t, true, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return nil, false, err
	}
	existing, err = scan(r.db.QueryRow(ctx,
		`SELECT `+columns+` FROM transactions WHERE user_id = $1 AND idempotency_key = $2`,
		t.UserID, t.IdempotencyKey))
	return existing, false, err
}

func (r *TransactionRepository) GetByID(ctx context.Context, id string) (*models.Transaction, error) {
	t, err := scan(r.db.QueryRow(ctx, `SELECT `+columns+` FROM transactions WHERE id = $1`, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	return t, err
}

// MarkCompleted и MarkFailed меняют только pending-записи: окончательный статус
// не перезаписывается, даже если исход фиксируют два участника сразу.
func (r *TransactionRepository) MarkCompleted(ctx context.Context, id string) error {
	_, err := r.db.Exec(ctx,
		`UPDATE transactions SET status = 'completed', updated_at = NOW() WHERE id = $1 AND status = 'pending'`, id)
	return err
}

func (r *TransactionRepository) MarkFailed(ctx context.Context, id, reason, message string) error {
	_, err := r.db.Exec(ctx, `
		UPDATE transactions SET status = 'failed', failure_reason = $2, failure_message = $3, updated_at = NOW()
		WHERE id = $1 AND status = 'pending'`, id, reason, message)
	return err
}

// ListByAccounts возвращает последние операции по счетам пользователя.
// Отклонённые операции видит только их инициатор.
func (r *TransactionRepository) ListByAccounts(ctx context.Context, userID string, accountIDs []string, limit int) ([]models.Transaction, error) {
	rows, err := r.db.Query(ctx, `
		SELECT `+columns+` FROM transactions
		WHERE (from_account_id = ANY($1) OR to_account_id = ANY($1))
		  AND (status <> 'failed' OR user_id = $2)
		ORDER BY created_at DESC
		LIMIT $3`, accountIDs, userID, limit)
	if err != nil {
		return nil, err
	}
	return scanAll(rows)
}

// ListStalePending возвращает pending-записи старше age — их исход должен уточнить recovery-воркер.
func (r *TransactionRepository) ListStalePending(ctx context.Context, age time.Duration, limit int) ([]models.Transaction, error) {
	rows, err := r.db.Query(ctx, `
		SELECT `+columns+` FROM transactions
		WHERE status = 'pending' AND created_at < NOW() - make_interval(secs => $1)
		ORDER BY created_at
		LIMIT $2`, age.Seconds(), limit)
	if err != nil {
		return nil, err
	}
	return scanAll(rows)
}
