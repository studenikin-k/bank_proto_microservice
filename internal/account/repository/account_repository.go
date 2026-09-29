package repository

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"math/big"

	"bank_proto_microservice/internal/account/models"
	"bank_proto_microservice/internal/utils"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

var (
	ErrAccountNotFound     = errors.New("счёт не найден")
	ErrAccountClosed       = errors.New("счёт закрыт")
	ErrInsufficientBalance = errors.New("недостаточно средств")
	SystemBankAccountID    = "00000000-0000-0000-0000-000000000001"
)

type AccountRepository struct {
	db *pgxpool.Pool
}

func NewAccountRepository(db *pgxpool.Pool) *AccountRepository {
	utils.LogSuccess("AccountRepository", "Инициализирован репозиторий счетов")
	return &AccountRepository{db: db}
}

func (r *AccountRepository) generateAccountID(ctx context.Context) (string, error) {
	const maxAttempts = 10
	for attempt := 0; attempt < maxAttempts; attempt++ {
		maxValue := big.NewInt(1_000_000_000_000)
		n, err := rand.Int(rand.Reader, maxValue)
		if err != nil {
			utils.LogError("AccountRepository", "Ошибка генерации случайного числа", err)
			return "", fmt.Errorf("ошибка генерации случайного числа: %w", err)
		}
		accountID := fmt.Sprintf("13%012d", n.Int64())

		var exists bool
		query := "SELECT EXISTS(SELECT 1 FROM accounts WHERE id = $1)"
		utils.LogDB("CHECK EXISTS", fmt.Sprintf("Проверка ID %s", accountID))

		err = r.db.QueryRow(ctx, query, accountID).Scan(&exists)
		if err != nil {
			utils.LogError("AccountRepository", fmt.Sprintf("Ошибка проверки коллизии ID %s", accountID), err)
			return "", fmt.Errorf("ошибка проверки уникальности: %w", err)
		}
		if !exists {
			return accountID, nil
		}
		utils.LogWarning("AccountRepository", "Коллизия ID счёта %s, попытка %d/%d", accountID, attempt+1, maxAttempts)
	}
	return "", errors.New("не удалось сгенерировать уникальный ID счёта")
}

func (r *AccountRepository) Create(ctx context.Context, userID string) (*models.Account, error) {
	accountID, err := r.generateAccountID(ctx)
	if err != nil {
		return nil, err
	}

	query := `
		INSERT INTO accounts (id, user_id, balance, status, created_at)
		VALUES ($1, $2, 100.00, 'active', NOW())
		RETURNING id, user_id, balance, status, created_at
	`
	utils.LogDB("INSERT ACCOUNT", fmt.Sprintf("Создание счёта %s для user_id %s", accountID, userID))

	var account models.Account
	err = r.db.QueryRow(ctx, query, accountID, userID).Scan(
		&account.ID, &account.UserID, &account.Balance, &account.Status, &account.CreatedAt,
	)
	if err != nil {
		utils.LogError("AccountRepository", fmt.Sprintf("Ошибка создания счёта в БД для %s", userID), err)
		return nil, fmt.Errorf("ошибка создания счёта в БД: %w", err)
	}

	utils.LogSuccess("AccountRepository", "Счёт создан в PostgreSQL: %s (баланс: %.2f)", account.ID, account.Balance)
	return &account, nil
}

func (r *AccountRepository) GetByID(ctx context.Context, accountID string) (*models.Account, error) {
	query := `SELECT id, user_id, balance, status, created_at FROM accounts WHERE id = $1`
	utils.LogDB("SELECT ACCOUNT", fmt.Sprintf("Поиск счёта: %s", accountID))

	var account models.Account
	err := r.db.QueryRow(ctx, query, accountID).Scan(
		&account.ID, &account.UserID, &account.Balance, &account.Status, &account.CreatedAt,
	)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			utils.LogWarning("AccountRepository", "Счёт %s не найден", accountID)
			return nil, ErrAccountNotFound
		}
		utils.LogError("AccountRepository", fmt.Sprintf("Ошибка получения счёта %s", accountID), err)
		return nil, err
	}
	return &account, nil
}

func (r *AccountRepository) GetByUserID(ctx context.Context, userID string) ([]models.Account, error) {
	query := `SELECT id, user_id, balance, status, created_at FROM accounts WHERE user_id = $1 ORDER BY created_at DESC`
	utils.LogDB("SELECT USER ACCOUNTS", fmt.Sprintf("Поиск счетов пользователя: %s", userID))

	rows, err := r.db.Query(ctx, query, userID)
	if err != nil {
		utils.LogError("AccountRepository", fmt.Sprintf("Ошибка получения счетов пользователя %s", userID), err)
		return nil, err
	}
	defer rows.Close()

	var accounts []models.Account
	for rows.Next() {
		var a models.Account
		if err := rows.Scan(&a.ID, &a.UserID, &a.Balance, &a.Status, &a.CreatedAt); err != nil {
			utils.LogError("AccountRepository", "Ошибка сканирования счёта", err)
			return nil, err
		}
		accounts = append(accounts, a)
	}
	return accounts, nil
}

func (r *AccountRepository) CountActiveAccountsByUserID(ctx context.Context, userID string) (int, error) {
	query := `SELECT COUNT(*) FROM accounts WHERE user_id = $1 AND status = 'active'`
	utils.LogDB("COUNT ACCOUNTS", fmt.Sprintf("Подсчёт счетов пользователя: %s", userID))

	var count int
	err := r.db.QueryRow(ctx, query, userID).Scan(&count)
	if err != nil {
		utils.LogError("AccountRepository", fmt.Sprintf("Ошибка подсчёта счетов пользователя %s", userID), err)
		return 0, err
	}
	return count, nil
}

func (r *AccountRepository) UpdateStatus(ctx context.Context, accountID, status string) error {
	query := `UPDATE accounts SET status = $1 WHERE id = $2`
	utils.LogDB("UPDATE STATUS", fmt.Sprintf("Обновление статуса счёта %s -> %s", accountID, status))

	res, err := r.db.Exec(ctx, query, status, accountID)
	if err != nil {
		utils.LogError("AccountRepository", fmt.Sprintf("Ошибка обновления статуса счёта %s", accountID), err)
		return err
	}
	if res.RowsAffected() == 0 {
		return ErrAccountNotFound
	}
	return nil
}

func (r *AccountRepository) ExecuteTransferTx(ctx context.Context, fromID, toID, feeID string, amount, totalDebit, feeAmount float64) error {
	utils.LogDB("BEGIN TX", fmt.Sprintf("Транзакция перевода: %s -> %s", fromID, toID))

	tx, err := r.db.Begin(ctx)
	if err != nil {
		utils.LogError("AccountRepository", "Ошибка старта транзакции", err)
		return err
	}
	defer tx.Rollback(ctx)

	// 1. Блокируем счет списания
	var fromBalance float64
	var fromStatus string
	queryFrom := `SELECT balance, status FROM accounts WHERE id = $1 FOR UPDATE`
	utils.LogDB("FOR UPDATE (FROM)", fmt.Sprintf("Блокировка счёта %s", fromID))

	err = tx.QueryRow(ctx, queryFrom, fromID).Scan(&fromBalance, &fromStatus)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			utils.LogWarning("AccountRepository", "Счёт списания %s не найден", fromID)
			return ErrAccountNotFound
		}
		return err
	}
	if fromStatus != "active" {
		utils.LogWarning("AccountRepository", "Счёт списания %s закрыт", fromID)
		return ErrAccountClosed
	}
	if fromBalance < totalDebit {
		utils.LogWarning("AccountRepository", "Недостаточно средств на счёте %s (баланс: %.2f, требуется: %.2f)", fromID, fromBalance, totalDebit)
		return ErrInsufficientBalance
	}

	// 2. Блокируем счет получателя
	var toStatus string
	queryTo := `SELECT status FROM accounts WHERE id = $1 FOR UPDATE`
	utils.LogDB("FOR UPDATE (TO)", fmt.Sprintf("Блокировка счёта получателя %s", toID))

	err = tx.QueryRow(ctx, queryTo, toID).Scan(&toStatus)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return errors.New("счёт получателя не найден")
		}
		return err
	}
	if toStatus != "active" {
		return errors.New("счёт получателя закрыт")
	}

	// 3. Списание
	utils.LogDB("DEBIT", fmt.Sprintf("Списание со счёта %s суммы %.2f", fromID, totalDebit))
	_, err = tx.Exec(ctx, `UPDATE accounts SET balance = balance - $1 WHERE id = $2`, totalDebit, fromID)
	if err != nil {
		return err
	}

	// 4. Начисление
	utils.LogDB("CREDIT", fmt.Sprintf("Начисление на счёт %s суммы %.2f", toID, amount))
	_, err = tx.Exec(ctx, `UPDATE accounts SET balance = balance + $1 WHERE id = $2`, amount, toID)
	if err != nil {
		return err
	}

	// 5. Комиссия
	if feeAmount > 0 {
		utils.LogDB("FEE", fmt.Sprintf("Начисление комиссии %.2f на системный счёт %s", feeAmount, feeID))
		_, err = tx.Exec(ctx, `UPDATE accounts SET balance = balance + $1 WHERE id = $2`, feeAmount, feeID)
		if err != nil {
			return err
		}
	}

	utils.LogDB("COMMIT", "Фиксация транзакции в БД")
	return tx.Commit(ctx)
}
