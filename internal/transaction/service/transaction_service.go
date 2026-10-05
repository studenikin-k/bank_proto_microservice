// Package service реализует сагу перевода денег между двумя сервисами с разными БД.
//
//  1. Transaction Service создаёт запись pending (id записи = transfer_id).
//  2. Account Service.ApplyTransfer в своей локальной транзакции двигает деньги
//     и записывает исход transfer_id (applied / aborted) — повторы с тем же id безопасны.
//  3. Transaction Service переводит запись в completed или failed.
//
// Если на шаге 2 исход неизвестен (таймаут, обрыв связи, падение процесса), запись
// остаётся pending. Её доводят до конца либо повтор клиента с тем же Idempotency-Key
// (шаг 2 повторяется идемпотентно), либо recovery-воркер: он вызывает ResolveTransfer,
// который окончательно фиксирует исход, и записывает его в свою БД.
package service

import (
	"context"
	"errors"
	"log/slog"
	"slices"
	"time"

	"bank_proto_microservice/internal/apperr"
	"bank_proto_microservice/internal/money"
	"bank_proto_microservice/internal/transaction/models"
	"bank_proto_microservice/internal/transaction/repository"
	"bank_proto_microservice/internal/transaction/worker"
	accountpb "bank_proto_microservice/proto/account"

	"github.com/google/uuid"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// SystemBankAccountID — системный счёт банка, получатель комиссий.
const SystemBankAccountID = "00000000-0000-0000-0000-000000000001"

const (
	DefaultHistoryLimit = 50
	MaxHistoryLimit     = 500
	maxIdempotencyKey   = 128
	// Время на запись итогового статуса после того, как исход стал известен.
	finishTimeout = 3 * time.Second
)

type Config struct {
	// Максимальное время вызова Account Service. Recovery-воркер трогает только записи
	// старше RecoveryAge, поэтому RecoveryAge должен быть заметно больше этого значения.
	AccountCallTimeout time.Duration
}

type TransactionService struct {
	repo     *repository.TransactionRepository
	accounts accountpb.AccountServiceClient
	pool     *worker.WorkerPool
	cfg      Config
}

func NewTransactionService(repo *repository.TransactionRepository, accounts accountpb.AccountServiceClient, pool *worker.WorkerPool, cfg Config) *TransactionService {
	return &TransactionService{repo: repo, accounts: accounts, pool: pool, cfg: cfg}
}

type CreateRequest struct {
	UserID         string
	IdempotencyKey string
	Type           string
	FromAccountID  string
	ToAccountID    string
	Amount         money.Amount
}

func invalid(msg string) error {
	return apperr.New(codes.InvalidArgument, apperr.ReasonInvalidArgument, msg)
}

func (s *TransactionService) newTransaction(req CreateRequest) (*models.Transaction, error) {
	if req.Type == "" {
		req.Type = models.TypeTransfer
	}
	percent, ok := models.FeePercent(req.Type)
	switch {
	case !ok:
		return nil, invalid("type должен быть transfer или payment")
	case uuid.Validate(req.UserID) != nil:
		return nil, invalid("user_id должен быть UUID")
	case req.FromAccountID == "" || req.ToAccountID == "":
		return nil, invalid("не указан счёт списания или зачисления")
	case req.FromAccountID == req.ToAccountID:
		return nil, invalid("нельзя переводить на тот же счёт")
	case req.Amount <= 0 || req.Amount > money.MaxAmount:
		return nil, invalid("сумма должна быть больше 0 и не больше " + money.MaxAmount.String())
	case len(req.IdempotencyKey) > maxIdempotencyKey:
		return nil, invalid("Idempotency-Key длиннее 128 символов")
	}

	key := req.IdempotencyKey
	if key == "" {
		key = uuid.NewString() // без ключа каждый запрос — новая операция
	}
	fee := money.Fee(req.Amount, int64(percent))
	return &models.Transaction{
		ID:             uuid.NewString(),
		UserID:         req.UserID,
		IdempotencyKey: key,
		Type:           req.Type,
		FromAccountID:  req.FromAccountID,
		ToAccountID:    req.ToAccountID,
		Amount:         req.Amount,
		FeePercent:     percent,
		FeeAmount:      fee,
		TotalDebit:     req.Amount + fee,
		FeeAccountID:   SystemBankAccountID,
		Status:         models.StatusPending,
	}, nil
}

// createPending создаёт запись pending или находит запись с тем же ключом идемпотентности.
func (s *TransactionService) createPending(ctx context.Context, req CreateRequest) (t *models.Transaction, replay bool, err error) {
	t, err = s.newTransaction(req)
	if err != nil {
		return nil, false, err
	}
	existing, created, err := s.repo.CreatePending(ctx, t)
	if err != nil {
		return nil, false, err
	}
	if created {
		return t, false, nil
	}
	if !existing.SameRequest(t) {
		return nil, false, apperr.New(codes.AlreadyExists, apperr.ReasonIdempotencyConflict,
			"Idempotency-Key уже использован для другой операции")
	}
	return existing, true, nil
}

// Create выполняет перевод синхронно. Повтор запроса с тем же ключом идемпотентности
// возвращает результат первого запроса, а незавершённую (pending) операцию доводит до конца.
func (s *TransactionService) Create(ctx context.Context, req CreateRequest) (*models.Transaction, bool, error) {
	t, replay, err := s.createPending(ctx, req)
	if err != nil {
		return nil, false, err
	}
	switch t.Status {
	case models.StatusCompleted:
		return t, replay, nil
	case models.StatusFailed:
		return t, replay, failureError(t)
	}
	t, err = s.finish(ctx, t, s.apply(ctx, t))
	return t, replay, err
}

// CreateAsync создаёт запись pending и передаёт исполнение Worker Pool.
// Клиент получает ID сразу, итог узнаёт через GetByID.
func (s *TransactionService) CreateAsync(ctx context.Context, req CreateRequest) (*models.Transaction, bool, error) {
	t, replay, err := s.createPending(ctx, req)
	if err != nil || replay {
		return t, replay, err // повтор: возвращаем текущее состояние, повторно не ставим в очередь
	}

	job := worker.Job{
		ID:   t.ID,
		Task: func(ctx context.Context) error { return s.apply(ctx, t) },
		// Повторяем только неизвестный исход: шаг идемпотентен по transfer_id, двойного
		// списания не будет. Определённый отказ повторять бессмысленно — он окончателен.
		RetryOn: func(err error) bool { return !apperr.IsDefinite(err) },
		OnDone: func(err error) {
			_, _ = s.finish(context.Background(), t, err)
		},
	}
	if err := s.pool.Submit(job); err != nil {
		// Задача не поставлена, Account Service её не видел — отказ окончательный.
		s.markFailed(ctx, t.ID, apperr.ReasonOverloaded, "очередь асинхронных переводов переполнена")
		return nil, false, apperr.New(codes.ResourceExhausted, apperr.ReasonOverloaded,
			"сервис перегружен, повторите запрос позже")
	}
	return t, false, nil
}

// apply — шаг 2 саги: вызов Account Service.
func (s *TransactionService) apply(ctx context.Context, t *models.Transaction) error {
	callCtx, cancel := context.WithTimeout(ctx, s.cfg.AccountCallTimeout)
	defer cancel()
	_, err := s.accounts.ApplyTransfer(callCtx, &accountpb.ApplyTransferRequest{
		TransferId:    t.ID,
		UserId:        t.UserID,
		FromAccountId: t.FromAccountID,
		ToAccountId:   t.ToAccountID,
		Amount:        t.Amount.Kopecks(),
		Fee:           t.FeeAmount.Kopecks(),
	})
	return err
}

// finish — шаг 3 саги: записывает исход шага 2.
func (s *TransactionService) finish(ctx context.Context, t *models.Transaction, applyErr error) (*models.Transaction, error) {
	// Исход уже известен — фиксируем его, даже если клиент успел отключиться.
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), finishTimeout)
	defer cancel()

	switch {
	case applyErr == nil:
		if err := s.repo.MarkCompleted(ctx, t.ID); err != nil {
			// Деньги уже переведены; статус completed допишет recovery-воркер.
			slog.Warn("перевод применён, но статус не записан", "transaction_id", t.ID, "err", err)
		}
		t.Status = models.StatusCompleted
		return t, nil

	case apperr.IsDefinite(applyErr):
		// Account Service зафиксировал отказ окончательно — перевод уже не применится.
		t.Status = models.StatusFailed
		t.FailureReason = apperr.Reason(applyErr)
		t.FailureMessage = status.Convert(applyErr).Message()
		s.markFailed(ctx, t.ID, t.FailureReason, t.FailureMessage)
		return t, applyErr

	default:
		code := codes.Unavailable
		if apperr.Code(applyErr) == codes.DeadlineExceeded {
			code = codes.DeadlineExceeded
		}
		slog.Warn("исход перевода неизвестен, запись остаётся pending",
			"transaction_id", t.ID, "code", apperr.Code(applyErr).String(), "err", applyErr)
		return t, apperr.New(code, apperr.ReasonOutcomeUnknown,
			"исход перевода пока неизвестен: он будет определён автоматически, статус можно проверить по transaction_id",
			apperr.MetaTransactionID, t.ID)
	}
}

func (s *TransactionService) markFailed(ctx context.Context, id, reason, msg string) {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), finishTimeout)
	defer cancel()
	if err := s.repo.MarkFailed(ctx, id, reason, msg); err != nil {
		// Запись останется pending; recovery-воркер зафиксирует отмену через ResolveTransfer.
		slog.Warn("не удалось записать статус failed", "transaction_id", id, "err", err)
	}
}

// failureError восстанавливает ошибку отклонённой операции для повторного запроса.
func failureError(t *models.Transaction) error {
	return apperr.New(apperr.CodeForReason(t.FailureReason), t.FailureReason, t.FailureMessage,
		apperr.MetaTransactionID, t.ID)
}

// RecoverStale доводит до конца pending-записи старше age: исход каждой определяет
// Account Service.ResolveTransfer (применён — completed, иначе отменяется — failed).
func (s *TransactionService) RecoverStale(ctx context.Context, age time.Duration, limit int) (int, error) {
	stale, err := s.repo.ListStalePending(ctx, age, limit)
	if err != nil {
		return 0, err
	}
	resolved := 0
	for i := range stale {
		t := &stale[i]
		callCtx, cancel := context.WithTimeout(ctx, s.cfg.AccountCallTimeout)
		resp, err := s.accounts.ResolveTransfer(callCtx, &accountpb.ResolveTransferRequest{TransferId: t.ID})
		cancel()
		if err != nil {
			slog.Warn("recovery: Account Service не ответил, повтор в следующем цикле", "transaction_id", t.ID, "err", err)
			continue
		}
		if resp.GetOutcome() == accountpb.ResolveTransferResponse_APPLIED {
			err = s.repo.MarkCompleted(ctx, t.ID)
		} else {
			err = s.repo.MarkFailed(ctx, t.ID, apperr.ReasonTransferAborted,
				"перевод отменён: его исход не был подтверждён вовремя")
		}
		if err != nil {
			slog.Warn("recovery: не удалось записать исход", "transaction_id", t.ID, "err", err)
			continue
		}
		resolved++
		slog.Info("recovery: исход перевода определён", "transaction_id", t.ID, "outcome", resp.GetOutcome().String())
	}
	return resolved, nil
}

// RunRecovery периодически запускает RecoverStale.
func (s *TransactionService) RunRecovery(ctx context.Context, interval, age time.Duration) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
		for {
			n, err := s.RecoverStale(ctx, age, 100)
			if err != nil {
				if ctx.Err() == nil {
					slog.Warn("recovery: ошибка чтения pending-записей", "err", err)
				}
				break
			}
			if n < 100 {
				break
			}
		}
	}
}

// History возвращает операции по счетам пользователя (или по одному его счёту).
func (s *TransactionService) History(ctx context.Context, userID, accountID string, limit int) ([]models.Transaction, error) {
	if limit <= 0 {
		limit = DefaultHistoryLimit
	}
	limit = min(limit, MaxHistoryLimit)

	ids, err := s.userAccountIDs(ctx, userID)
	if err != nil {
		return nil, err
	}
	if accountID != "" {
		if !slices.Contains(ids, accountID) {
			return nil, apperr.New(codes.PermissionDenied, apperr.ReasonAccountForbidden, "нет доступа к данному счёту")
		}
		ids = []string{accountID}
	}
	if len(ids) == 0 {
		return []models.Transaction{}, nil
	}
	return s.repo.ListByAccounts(ctx, userID, ids, limit)
}

// Get возвращает операцию, если пользователь её инициатор, отправитель или получатель.
// Чужие операции выглядят как несуществующие, чтобы не раскрывать их наличие.
func (s *TransactionService) Get(ctx context.Context, userID, id string) (*models.Transaction, error) {
	notFound := apperr.New(codes.NotFound, apperr.ReasonTransactionNotFound, "транзакция не найдена")
	if uuid.Validate(id) != nil {
		return nil, notFound
	}
	t, err := s.repo.GetByID(ctx, id)
	if errors.Is(err, repository.ErrNotFound) {
		return nil, notFound
	}
	if err != nil {
		return nil, err
	}
	if t.UserID == userID {
		return t, nil
	}
	if t.Status == models.StatusFailed {
		return nil, notFound
	}
	ids, err := s.userAccountIDs(ctx, userID)
	if err != nil {
		return nil, err
	}
	if slices.Contains(ids, t.FromAccountID) || slices.Contains(ids, t.ToAccountID) {
		return t, nil
	}
	return nil, notFound
}

func (s *TransactionService) userAccountIDs(ctx context.Context, userID string) ([]string, error) {
	callCtx, cancel := context.WithTimeout(ctx, s.cfg.AccountCallTimeout)
	defer cancel()
	resp, err := s.accounts.GetUserAccounts(callCtx, &accountpb.GetUserAccountsRequest{UserId: userID})
	if err != nil {
		return nil, err
	}
	ids := make([]string, 0, len(resp.GetAccounts()))
	for _, a := range resp.GetAccounts() {
		ids = append(ids, a.GetId())
	}
	return ids, nil
}
