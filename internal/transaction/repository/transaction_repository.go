package repository

import (
	"context"
	"errors"
	"fmt"

	"bank_proto_microservice/internal/transaction/models"
	"bank_proto_microservice/internal/utils"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

type TransactionRepository struct {
	db *pgxpool.Pool
}

func NewTransactionRepository(db *pgxpool.Pool) *TransactionRepository {
	utils.LogSuccess("TransactionRepository", "Инициализирован репозиторий транзакций")
	return &TransactionRepository{db: db}
}

func (r *TransactionRepository) Create(
	ctx context.Context,
	txType, fromAccountID, toAccountID, feeAccountID string,
	amount, feeAmount, totalDebit float64,
	feePercent int,
	status string,
) (*models.Transaction, error) {

	txID := uuid.New().String()
	query := `
		INSERT INTO transactions (
			id, type, from_account_id, to_account_id,
			amount, fee_percent, fee_amount, total_debit,
			fee_account_id, status, created_at
		) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, NOW())
		RETURNING id, type, from_account_id, to_account_id, amount,
		          fee_percent, fee_amount, total_debit, fee_account_id,
		          status, created_at
	`
	utils.LogDB("INSERT TRANSACTION", fmt.Sprintf("Запись транзакции %s: %s -> %s (сумма: %.2f)", txID, fromAccountID, toAccountID, amount))

	var tx models.Transaction
	err := r.db.QueryRow(ctx, query,
		txID, txType, fromAccountID, toAccountID,
		amount, feePercent, feeAmount, totalDebit,
		feeAccountID, status,
	).Scan(
		&tx.ID, &tx.Type, &tx.FromAccountID, &tx.ToAccountID,
		&tx.Amount, &tx.FeePercent, &tx.FeeAmount, &tx.TotalDebit,
		&tx.FeeAccountID, &tx.Status, &tx.CreatedAt,
	)

	if err != nil {
		utils.LogError("TransactionRepository", fmt.Sprintf("Ошибка сохранения транзакции %s в БД", txID), err)
		return nil, err
	}

	utils.LogSuccess("TransactionRepository", "Транзакция сохранена в PostgreSQL: %s (статус: %s)", tx.ID, tx.Status)
	return &tx, nil
}

func (r *TransactionRepository) GetByID(ctx context.Context, transactionID string) (*models.Transaction, error) {
	query := `
		SELECT id, type, from_account_id, to_account_id, amount,
		       fee_percent, fee_amount, total_debit, fee_account_id,
		       status, created_at
		FROM transactions
		WHERE id = $1
	`
	utils.LogDB("SELECT TRANSACTION", fmt.Sprintf("Поиск транзакции: %s", transactionID))

	var tx models.Transaction
	err := r.db.QueryRow(ctx, query, transactionID).Scan(
		&tx.ID, &tx.Type, &tx.FromAccountID, &tx.ToAccountID,
		&tx.Amount, &tx.FeePercent, &tx.FeeAmount, &tx.TotalDebit,
		&tx.FeeAccountID, &tx.Status, &tx.CreatedAt,
	)

	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			utils.LogWarning("TransactionRepository", "Транзакция %s не найдена", transactionID)
			return nil, errors.New("транзакция не найдена")
		}
		utils.LogError("TransactionRepository", fmt.Sprintf("Ошибка получения транзакции %s", transactionID), err)
		return nil, err
	}

	return &tx, nil
}

func (r *TransactionRepository) GetByAccountID(ctx context.Context, accountID string) ([]models.Transaction, error) {
	query := `
		SELECT id, type, from_account_id, to_account_id, amount,
		       fee_percent, fee_amount, total_debit, fee_account_id,
		       status, created_at
		FROM transactions
		WHERE from_account_id = $1 OR to_account_id = $1
		ORDER BY created_at DESC
	`
	utils.LogDB("SELECT TRANSACTIONS BY ACCOUNT", fmt.Sprintf("Поиск истории для счёта: %s", accountID))

	rows, err := r.db.Query(ctx, query, accountID)
	if err != nil {
		utils.LogError("TransactionRepository", fmt.Sprintf("Ошибка получения истории для счёта %s", accountID), err)
		return nil, err
	}
	defer rows.Close()

	var transactions []models.Transaction
	for rows.Next() {
		var tx models.Transaction
		err := rows.Scan(
			&tx.ID, &tx.Type, &tx.FromAccountID, &tx.ToAccountID,
			&tx.Amount, &tx.FeePercent, &tx.FeeAmount, &tx.TotalDebit,
			&tx.FeeAccountID, &tx.Status, &tx.CreatedAt,
		)
		if err != nil {
			utils.LogError("TransactionRepository", "Ошибка сканирования строки транзакции", err)
			return nil, err
		}
		transactions = append(transactions, tx)
	}

	return transactions, nil
}
