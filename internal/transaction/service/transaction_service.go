package service

import (
	"context"
	"errors"
	"fmt"

	"bank_proto_microservice/internal/transaction/models"
	"bank_proto_microservice/internal/transaction/repository"
	"bank_proto_microservice/internal/transaction/worker"
	"bank_proto_microservice/internal/utils"
	accountpb "bank_proto_microservice/proto/account"
)

const SystemBankAccountID = "00000000-0000-0000-0000-000000000001"

var (
	ErrInvalidAmount = errors.New("сумма должна быть больше 0")
	ErrSelfTransfer  = errors.New("нельзя переводить на свой же счёт")
)

type TransactionService struct {
	txRepo        *repository.TransactionRepository
	accountClient accountpb.AccountServiceClient // gRPC-клиент к сервису счетов
	workerPool    *worker.WorkerPool
}

func NewTransactionService(
	txRepo *repository.TransactionRepository,
	accountClient accountpb.AccountServiceClient,
	workerPool *worker.WorkerPool,
) *TransactionService {
	utils.LogSuccess("TransactionService", "Инициализирован сервис транзакций с gRPC-клиентом счетов")
	return &TransactionService{
		txRepo:        txRepo,
		accountClient: accountClient,
		workerPool:    workerPool,
	}
}

func (s *TransactionService) Transfer(ctx context.Context, userID, fromAccountID, toAccountID string, amount float64) (*models.Transaction, error) {
	utils.LogInfo("TransactionService", "Обработка перевода (1%%): %s -> %s (Сумма: %.2f) от User: %s", fromAccountID, toAccountID, amount, userID)

	if amount <= 0 {
		return nil, ErrInvalidAmount
	}
	if fromAccountID == toAccountID {
		return nil, ErrSelfTransfer
	}

	feeAmount := amount * 0.01
	totalDebit := amount + feeAmount

	// 1. Вызов gRPC в Account Service для списания/начисления денег
	utils.LogInfo("TransactionService", "Вызов gRPC AccountService.UpdateBalance...")
	resp, err := s.accountClient.UpdateBalance(ctx, &accountpb.UpdateBalanceRequest{
		FromAccountId: fromAccountID,
		ToAccountId:   toAccountID,
		Amount:        amount,
		FeeAmount:     feeAmount,
		TotalDebit:    totalDebit,
		FeeAccountId:  SystemBankAccountID,
	})

	if err != nil {
		utils.LogError("TransactionService", "gRPC ошибка вызова AccountService", err)
		return nil, fmt.Errorf("ошибка связи с сервисом счетов: %w", err)
	}

	if !resp.GetSuccess() {
		utils.LogWarning("TransactionService", "Отказ в переводе от AccountService: %s", resp.GetErrorMessage())
		return nil, errors.New(resp.GetErrorMessage())
	}

	// 2. Запись транзакции в свою базу PostgreSQL (Transactions DB)
	tx, err := s.txRepo.Create(ctx, "transfer", fromAccountID, toAccountID, SystemBankAccountID, amount, feeAmount, totalDebit, 1, "completed")
	if err != nil {
		return nil, err
	}

	utils.LogSuccess("TransactionService", "Перевод %s успешно завершён", tx.ID)
	return tx, nil
}

func (s *TransactionService) Payment(ctx context.Context, userID, fromAccountID, toAccountID string, amount float64) (*models.Transaction, error) {
	utils.LogInfo("TransactionService", "Обработка платежа (3%%): %s -> %s (Сумма: %.2f) от User: %s", fromAccountID, toAccountID, amount, userID)

	if amount <= 0 {
		return nil, ErrInvalidAmount
	}
	if fromAccountID == toAccountID {
		return nil, ErrSelfTransfer
	}

	feeAmount := amount * 0.03
	totalDebit := amount + feeAmount

	resp, err := s.accountClient.UpdateBalance(ctx, &accountpb.UpdateBalanceRequest{
		FromAccountId: fromAccountID,
		ToAccountId:   toAccountID,
		Amount:        amount,
		FeeAmount:     feeAmount,
		TotalDebit:    totalDebit,
		FeeAccountId:  SystemBankAccountID,
	})

	if err != nil {
		utils.LogError("TransactionService", "gRPC ошибка вызова AccountService при платеже", err)
		return nil, fmt.Errorf("ошибка связи с сервисом счетов: %w", err)
	}

	if !resp.GetSuccess() {
		utils.LogWarning("TransactionService", "Отказ в платеже от AccountService: %s", resp.GetErrorMessage())
		return nil, errors.New(resp.GetErrorMessage())
	}

	tx, err := s.txRepo.Create(ctx, "payment", fromAccountID, toAccountID, SystemBankAccountID, amount, feeAmount, totalDebit, 3, "completed")
	if err != nil {
		return nil, err
	}

	utils.LogSuccess("TransactionService", "Платёж %s успешно завершён", tx.ID)
	return tx, nil
}

// TransferAsync ставит задачу в Worker Pool
func (s *TransactionService) TransferAsync(userID, fromAccountID, toAccountID string, amount float64) (string, error) {
	taskID := fmt.Sprintf("task-%s-%d", userID, worker.GetCurrentTimeMs())
	utils.LogInfo("TransactionService", "Постановка асинхронного перевода %s в Worker Pool", taskID)

	job := worker.Job{
		ID: taskID,
		Task: func() error {
			// Воркер выполняет перевод в фоновом контексте
			_, err := s.Transfer(context.Background(), userID, fromAccountID, toAccountID, amount)
			return err
		},
	}

	if err := s.workerPool.Submit(job); err != nil {
		utils.LogError("TransactionService", fmt.Sprintf("Очередь воркеров отклонила задачу %s", taskID), err)
		return "", err
	}

	utils.LogSuccess("TransactionService", "Задача %s успешно передана в очередь обработки", taskID)
	return taskID, nil
}

func (s *TransactionService) GetHistory(ctx context.Context, accountID string) ([]models.Transaction, error) {
	utils.LogInfo("TransactionService", "Запрос истории операций для счёта: %s", accountID)
	return s.txRepo.GetByAccountID(ctx, accountID)
}

func (s *TransactionService) GetByID(ctx context.Context, transactionID string) (*models.Transaction, error) {
	utils.LogInfo("TransactionService", "Запрос транзакции по ID: %s", transactionID)
	return s.txRepo.GetByID(ctx, transactionID)
}
