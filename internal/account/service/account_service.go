package service

import (
	"context"
	"errors"
	"fmt"
	"strconv"

	"bank_proto_microservice/internal/account/cache"
	"bank_proto_microservice/internal/account/models"
	"bank_proto_microservice/internal/account/repository"
	"bank_proto_microservice/internal/utils"

	"github.com/redis/go-redis/v9"
)

var (
	ErrUnauthorizedAccess   = errors.New("нет доступа к данному счёту")
	ErrAccountAlreadyClosed = errors.New("счёт уже закрыт")
	ErrAccountLimitReached  = errors.New("достигнут лимит активных счетов (максимум 5)")
)

const MaxActiveAccounts = 5

type AccountService struct {
	accountRepo *repository.AccountRepository
	cache       *cache.RedisCache
}

func NewAccountService(accountRepo *repository.AccountRepository, cache *cache.RedisCache) *AccountService {
	utils.LogSuccess("AccountService", "Инициализирован сервис счетов")
	return &AccountService{
		accountRepo: accountRepo,
		cache:       cache,
	}
}

func (s *AccountService) CreateAccount(ctx context.Context, userID string) (*models.Account, error) {
	utils.LogInfo("AccountService", "Создание нового счёта для пользователя %s", userID)

	activeCount, err := s.accountRepo.CountActiveAccountsByUserID(ctx, userID)
	if err != nil {
		utils.LogError("AccountService", "Ошибка проверки лимита счетов", err)
		return nil, err
	}

	if activeCount >= MaxActiveAccounts {
		utils.LogWarning("AccountService", "Пользователь %s достиг лимита активных счетов (%d/%d)", userID, activeCount, MaxActiveAccounts)
		return nil, ErrAccountLimitReached
	}

	account, err := s.accountRepo.Create(ctx, userID)
	if err != nil {
		utils.LogError("AccountService", fmt.Sprintf("Ошибка создания счёта для пользователя %s", userID), err)
		return nil, err
	}

	if s.cache != nil {
		_ = s.cache.Delete(ctx, cache.UserAccountsKey(userID))
		utils.LogInfo("Cache", "Инвалидирован кеш списка счетов пользователя %s", userID)
	}

	utils.LogSuccess("AccountService", "Счёт %s успешно создан для пользователя %s (баланс: %.2f)", account.ID, userID, account.Balance)
	return account, nil
}

func (s *AccountService) GetAccount(ctx context.Context, accountID, userID string) (*models.Account, error) {
	utils.LogInfo("AccountService", "Получение информации о счёте %s", accountID)

	var account *models.Account
	var err error

	if s.cache != nil {
		balanceKey := cache.AccountBalanceKey(accountID)
		balanceStr, cacheErr := s.cache.Get(ctx, balanceKey)
		if cacheErr == nil {
			utils.LogSuccess("Cache", "HIT: Баланс счёта %s найден в кеше: %s", accountID, balanceStr)
			account, err = s.accountRepo.GetByID(ctx, accountID)
			if err != nil {
				utils.LogError("AccountService", fmt.Sprintf("Счёт %s не найден", accountID), err)
				return nil, repository.ErrAccountNotFound
			}
			if bal, parseErr := strconv.ParseFloat(balanceStr, 64); parseErr == nil {
				account.Balance = bal
			}
		} else if !errors.Is(cacheErr, redis.Nil) {
			utils.LogWarning("Cache", "Ошибка чтения из кеша: %v", cacheErr)
		} else {
			utils.LogInfo("Cache", "MISS: Баланс счёта %s не найден в кеше", accountID)
		}
	}

	if account == nil {
		account, err = s.accountRepo.GetByID(ctx, accountID)
		if err != nil {
			utils.LogError("AccountService", fmt.Sprintf("Счёт %s не найден", accountID), err)
			return nil, repository.ErrAccountNotFound
		}
		if s.cache != nil {
			balanceKey := cache.AccountBalanceKey(accountID)
			_ = s.cache.Set(ctx, balanceKey, fmt.Sprintf("%.2f", account.Balance), cache.AccountBalanceTTL)
			utils.LogSuccess("Cache", "Баланс счёта %s сохранён в кеш: %.2f (TTL: %v)", accountID, account.Balance, cache.AccountBalanceTTL)
		}
	}

	if account.UserID != userID {
		utils.LogWarning("AccountService", "Попытка доступа к чужому счёту %s пользователем %s", accountID, userID)
		return nil, ErrUnauthorizedAccess
	}
	if account.Status != "active" {
		utils.LogWarning("AccountService", "Попытка доступа к закрытому счёту %s", accountID)
		return nil, repository.ErrAccountClosed
	}

	utils.LogSuccess("AccountService", "Информация о счёте %s получена (баланс: %.2f)", accountID, account.Balance)
	return account, nil
}

func (s *AccountService) GetUserAccounts(ctx context.Context, userID string) ([]models.Account, error) {
	utils.LogInfo("AccountService", "Получение списка счетов пользователя %s", userID)

	if s.cache != nil {
		cacheKey := cache.UserAccountsKey(userID)
		var accounts []models.Account
		if err := s.cache.GetJSON(ctx, cacheKey, &accounts); err == nil {
			utils.LogSuccess("Cache", "HIT: Список счетов пользователя %s получен из кеша (%d счетов)", userID, len(accounts))
			return accounts, nil
		}
		utils.LogInfo("Cache", "MISS: Список счетов пользователя %s не найден в кеше", userID)
	}

	accounts, err := s.accountRepo.GetByUserID(ctx, userID)
	if err != nil {
		utils.LogError("AccountService", fmt.Sprintf("Ошибка получения счетов пользователя %s", userID), err)
		return nil, err
	}

	if s.cache != nil {
		cacheKey := cache.UserAccountsKey(userID)
		_ = s.cache.SetJSON(ctx, cacheKey, accounts, cache.UserAccountsTTL)
		utils.LogSuccess("Cache", "Список счетов пользователя %s сохранён в кеш (TTL: %v)", userID, cache.UserAccountsTTL)
	}
	return accounts, nil
}

func (s *AccountService) DeleteAccount(ctx context.Context, accountID, userID string) error {
	utils.LogInfo("AccountService", "Закрытие счёта %s пользователем %s", accountID, userID)

	account, err := s.accountRepo.GetByID(ctx, accountID)
	if err != nil {
		utils.LogError("AccountService", fmt.Sprintf("Счёт %s не найден", accountID), err)
		return repository.ErrAccountNotFound
	}
	if account.UserID != userID {
		utils.LogWarning("AccountService", "Попытка закрыть чужой счёт %s пользователем %s", accountID, userID)
		return ErrUnauthorizedAccess
	}
	if account.Status == "closed" {
		utils.LogWarning("AccountService", "Счёт %s уже закрыт", accountID)
		return ErrAccountAlreadyClosed
	}

	if err := s.accountRepo.UpdateStatus(ctx, accountID, "closed"); err != nil {
		utils.LogError("AccountService", fmt.Sprintf("Ошибка изменения статуса счёта %s", accountID), err)
		return err
	}

	if s.cache != nil {
		_ = s.cache.Delete(ctx, cache.AccountBalanceKey(accountID), cache.UserAccountsKey(userID))
		utils.LogInfo("Cache", "Инвалидирован кеш для счёта %s и пользователя %s", accountID, userID)
	}

	utils.LogSuccess("AccountService", "Счёт %s успешно закрыт", accountID)
	return nil
}

func (s *AccountService) UpdateBalance(ctx context.Context, fromID, toID, feeID string, amount, totalDebit, feeAmount float64) error {
	utils.LogInfo("AccountService", "Выполнение списания: %s -> %s (Сумма: %.2f, Списание: %.2f)", fromID, toID, amount, totalDebit)

	err := s.accountRepo.ExecuteTransferTx(ctx, fromID, toID, feeID, amount, totalDebit, feeAmount)
	if err != nil {
		utils.LogError("AccountService", "Ошибка ExecuteTransferTx", err)
		return err
	}

	if s.cache != nil {
		_ = s.cache.Delete(ctx, cache.AccountBalanceKey(fromID), cache.AccountBalanceKey(toID))
		utils.LogInfo("Cache", "Инвалидирован кеш балансов для счетов %s и %s", fromID, toID)
	}

	utils.LogSuccess("AccountService", "Балансы успешно обновлены для счетов %s и %s", fromID, toID)
	return nil
}
