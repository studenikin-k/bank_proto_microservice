package service

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"bank_proto_microservice/internal/account/cache"
	"bank_proto_microservice/internal/account/models"
	"bank_proto_microservice/internal/account/repository"
	"bank_proto_microservice/internal/money"

	"github.com/google/uuid"
)

// ErrInvalid — некорректные входные данные.
var ErrInvalid = errors.New("некорректный запрос")

const MaxActiveAccounts = 5

type AccountService struct {
	repo           *repository.AccountRepository
	cache          *cache.RedisCache // nil — кеш выключен
	openingBalance money.Amount
}

func NewAccountService(repo *repository.AccountRepository, c *cache.RedisCache, openingBalance money.Amount) *AccountService {
	return &AccountService{repo: repo, cache: c, openingBalance: openingBalance}
}

func invalid(format string, args ...any) error {
	return fmt.Errorf("%w: %s", ErrInvalid, fmt.Sprintf(format, args...))
}

func (s *AccountService) CreateAccount(ctx context.Context, userID string) (*models.Account, error) {
	if uuid.Validate(userID) != nil {
		return nil, invalid("user_id должен быть UUID")
	}
	acc, err := s.repo.Create(ctx, userID, s.openingBalance, MaxActiveAccounts)
	if err != nil {
		return nil, err
	}
	s.invalidate(ctx, cache.UserAccountsKey(userID))
	return acc, nil
}

// GetAccount возвращает счёт, если он принадлежит пользователю.
func (s *AccountService) GetAccount(ctx context.Context, accountID, userID string) (*models.Account, error) {
	var acc models.Account
	key := cache.AccountKey(accountID)
	hit, err := s.cache.GetJSON(ctx, key, &acc)
	if err != nil {
		slog.Debug("ошибка чтения кеша", "key", key, "err", err)
	}
	if !hit {
		dbAcc, err := s.repo.GetByID(ctx, accountID)
		if err != nil {
			return nil, err
		}
		acc = *dbAcc
		if err := s.cache.SetJSON(ctx, key, acc); err != nil {
			slog.Debug("ошибка записи в кеш", "key", key, "err", err)
		}
	}
	if acc.UserID != userID {
		return nil, repository.ErrForbidden
	}
	return &acc, nil
}

func (s *AccountService) GetUserAccounts(ctx context.Context, userID string) ([]models.Account, error) {
	if uuid.Validate(userID) != nil {
		return nil, invalid("user_id должен быть UUID")
	}
	key := cache.UserAccountsKey(userID)
	var accounts []models.Account
	hit, err := s.cache.GetJSON(ctx, key, &accounts)
	if err != nil {
		slog.Debug("ошибка чтения кеша", "key", key, "err", err)
	}
	if hit {
		return accounts, nil
	}

	accounts, err = s.repo.ListByUser(ctx, userID)
	if err != nil {
		return nil, err
	}
	if err := s.cache.SetJSON(ctx, key, accounts); err != nil {
		slog.Debug("ошибка записи в кеш", "key", key, "err", err)
	}
	return accounts, nil
}

func (s *AccountService) CloseAccount(ctx context.Context, accountID, userID string) error {
	if err := s.repo.Close(ctx, accountID, userID); err != nil {
		return err
	}
	s.invalidate(ctx, cache.AccountKey(accountID), cache.UserAccountsKey(userID))
	return nil
}

// ApplyTransfer — шаг саги перевода (см. AccountRepository.ApplyTransfer).
func (s *AccountService) ApplyTransfer(ctx context.Context, t models.Transfer) (*models.ApplyResult, error) {
	switch {
	case uuid.Validate(t.ID) != nil:
		return nil, invalid("transfer_id должен быть UUID")
	case uuid.Validate(t.UserID) != nil:
		return nil, invalid("user_id должен быть UUID")
	case t.FromID == "" || t.ToID == "":
		return nil, invalid("не указан счёт списания или зачисления")
	case t.FromID == t.ToID:
		return nil, invalid("нельзя переводить на тот же счёт")
	case t.Amount <= 0 || t.Amount > money.MaxAmount:
		return nil, invalid("сумма перевода должна быть больше 0 и не больше %s", money.MaxAmount)
	case t.Fee < 0 || t.Fee > t.Amount:
		return nil, invalid("некорректная комиссия")
	}

	res, err := s.repo.ApplyTransfer(ctx, t)
	if err != nil {
		return nil, err
	}
	if !res.AlreadyApplied {
		// Удаляем ключи после COMMIT: следующее чтение возьмёт свежий баланс из БД.
		s.invalidate(ctx,
			cache.AccountKey(t.FromID), cache.AccountKey(t.ToID),
			cache.UserAccountsKey(res.FromUserID), cache.UserAccountsKey(res.ToUserID))
	}
	return res, nil
}

func (s *AccountService) ResolveTransfer(ctx context.Context, transferID string) (applied bool, err error) {
	if uuid.Validate(transferID) != nil {
		return false, invalid("transfer_id должен быть UUID")
	}
	return s.repo.ResolveTransfer(ctx, transferID)
}

// RunFeeSweeper периодически переносит собранные комиссии на системный счёт.
func (s *AccountService) RunFeeSweeper(ctx context.Context, interval time.Duration) {
	if interval <= 0 {
		return
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
		for {
			sweepCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
			n, total, err := s.repo.SweepFees(sweepCtx, 10000)
			cancel()
			if err != nil {
				if ctx.Err() == nil {
					slog.Warn("перенос комиссий не удался", "err", err)
				}
				break
			}
			if n > 0 {
				s.invalidate(ctx, cache.AccountKey(repository.SystemBankAccountID))
				slog.Debug("комиссии перенесены на системный счёт", "transfers", n, "total", total.String())
			}
			if n < 10000 {
				break
			}
		}
	}
}

func (s *AccountService) invalidate(ctx context.Context, keys ...string) {
	if err := s.cache.Delete(context.WithoutCancel(ctx), keys...); err != nil {
		slog.Warn("не удалось инвалидировать кеш, данные могут устареть на время TTL", "keys", keys, "err", err)
	}
}
