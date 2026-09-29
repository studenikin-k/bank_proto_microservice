package grpc

import (
	"context"
	"errors"
	"time"

	"bank_proto_microservice/internal/account/repository"
	"bank_proto_microservice/internal/account/service"
	"bank_proto_microservice/internal/utils"
	accountpb "bank_proto_microservice/proto/account"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

type AccountServer struct {
	accountpb.UnimplementedAccountServiceServer
	svc *service.AccountService
}

func NewAccountServer(svc *service.AccountService) *AccountServer {
	utils.LogSuccess("AccountServer", "Инициализирован gRPC сервер AccountService")
	return &AccountServer{svc: svc}
}

func (s *AccountServer) CreateAccount(ctx context.Context, req *accountpb.CreateAccountRequest) (*accountpb.AccountResponse, error) {
	userID := req.GetUserId()
	utils.LogInfo("AccountServer", "Запрос на создание счёта от пользователя: %s", userID)

	if userID == "" {
		utils.LogWarning("AccountServer", "user_id не найден в запросе")
		return nil, status.Error(codes.InvalidArgument, "user_id обязателен")
	}

	account, err := s.svc.CreateAccount(ctx, userID)
	if err != nil {
		utils.LogError("AccountServer", "Ошибка создания счёта", err)
		if errors.Is(err, service.ErrAccountLimitReached) {
			return nil, status.Error(codes.ResourceExhausted, "Достигнут лимит активных счетов (максимум 5)")
		}
		return nil, status.Errorf(codes.Internal, "Ошибка создания счёта: %v", err)
	}

	utils.LogSuccess("AccountServer", "Счёт успешно создан: %s", account.ID)

	return &accountpb.AccountResponse{
		Id:        account.ID,
		UserId:    account.UserID,
		Balance:   account.Balance,
		Status:    account.Status,
		CreatedAt: account.CreatedAt.Format(time.RFC3339),
	}, nil
}

func (s *AccountServer) GetAccount(ctx context.Context, req *accountpb.GetAccountRequest) (*accountpb.AccountResponse, error) {
	accID := req.GetAccountId()
	userID := req.GetUserId()
	utils.LogInfo("AccountServer", "Запрос информации о счёте: %s (User: %s)", accID, userID)

	account, err := s.svc.GetAccount(ctx, accID, userID)
	if err != nil {
		utils.LogError("AccountServer", "Ошибка получения счёта", err)
		if errors.Is(err, repository.ErrAccountNotFound) {
			return nil, status.Error(codes.NotFound, "Счёт не найден")
		}
		if errors.Is(err, service.ErrUnauthorizedAccess) {
			return nil, status.Error(codes.PermissionDenied, "Нет доступа к данному счёту")
		}
		if errors.Is(err, repository.ErrAccountClosed) {
			return nil, status.Error(codes.FailedPrecondition, "Счёт закрыт")
		}
		return nil, status.Errorf(codes.Internal, "Ошибка получения счёта: %v", err)
	}

	utils.LogSuccess("AccountServer", "Информация о счёте отправлена: %s", accID)

	return &accountpb.AccountResponse{
		Id:        account.ID,
		UserId:    account.UserID,
		Balance:   account.Balance,
		Status:    account.Status,
		CreatedAt: account.CreatedAt.Format(time.RFC3339),
	}, nil
}

func (s *AccountServer) GetUserAccounts(ctx context.Context, req *accountpb.GetUserAccountsRequest) (*accountpb.AccountListResponse, error) {
	userID := req.GetUserId()
	utils.LogInfo("AccountServer", "Запрос списка счетов от пользователя: %s", userID)

	accounts, err := s.svc.GetUserAccounts(ctx, userID)
	if err != nil {
		utils.LogError("AccountServer", "Ошибка получения счетов", err)
		return nil, status.Errorf(codes.Internal, "Ошибка получения счетов: %v", err)
	}

	var res []*accountpb.AccountResponse
	activeCount := 0
	closedCount := 0

	for _, a := range accounts {
		res = append(res, &accountpb.AccountResponse{
			Id:        a.ID,
			UserId:    a.UserID,
			Balance:   a.Balance,
			Status:    a.Status,
			CreatedAt: a.CreatedAt.Format(time.RFC3339),
		})
		if a.Status == "active" {
			activeCount++
		} else {
			closedCount++
		}
	}

	utils.LogSuccess("AccountServer", "Отправлен список счетов: %d шт. (активных: %d, закрытых: %d)", len(accounts), activeCount, closedCount)

	return &accountpb.AccountListResponse{
		Accounts:      res,
		Total:         int32(len(accounts)),
		ActiveCount:   int32(activeCount),
		ClosedCount:   int32(closedCount),
		MaxAccounts:   5,
		CanCreateMore: activeCount < 5,
	}, nil
}

func (s *AccountServer) DeleteAccount(ctx context.Context, req *accountpb.DeleteAccountRequest) (*accountpb.DeleteAccountResponse, error) {
	accID := req.GetAccountId()
	userID := req.GetUserId()
	utils.LogInfo("AccountServer", "Запрос на закрытие счёта: %s (User: %s)", accID, userID)

	err := s.svc.DeleteAccount(ctx, accID, userID)
	if err != nil {
		utils.LogError("AccountServer", "Ошибка закрытия счёта", err)
		if errors.Is(err, repository.ErrAccountNotFound) {
			return nil, status.Error(codes.NotFound, "Счёт не найден")
		}
		if errors.Is(err, service.ErrUnauthorizedAccess) {
			return nil, status.Error(codes.PermissionDenied, "Нет доступа к данному счёту")
		}
		if errors.Is(err, service.ErrAccountAlreadyClosed) {
			return nil, status.Error(codes.FailedPrecondition, "Счёт уже закрыт")
		}
		return nil, status.Errorf(codes.Internal, "Ошибка закрытия счёта: %v", err)
	}

	utils.LogSuccess("AccountServer", "Счёт успешно закрыт: %s", accID)

	return &accountpb.DeleteAccountResponse{
		Success:   true,
		Message:   "Счёт успешно закрыт",
		AccountId: accID,
	}, nil
}

func (s *AccountServer) UpdateBalance(ctx context.Context, req *accountpb.UpdateBalanceRequest) (*accountpb.UpdateBalanceResponse, error) {
	utils.LogInfo("AccountServer", "Запрос на перевод: From=%s, To=%s, Amount=%.2f", req.GetFromAccountId(), req.GetToAccountId(), req.GetAmount())

	err := s.svc.UpdateBalance(
		ctx,
		req.GetFromAccountId(),
		req.GetToAccountId(),
		req.GetFeeAccountId(),
		req.GetAmount(),
		req.GetTotalDebit(),
		req.GetFeeAmount(),
	)
	if err != nil {
		utils.LogError("AccountServer", "Ошибка выполнения перевода", err)
		return &accountpb.UpdateBalanceResponse{Success: false, ErrorMessage: err.Error()}, nil
	}

	utils.LogSuccess("AccountServer", "Перевод успешно выполнен: From=%s -> To=%s", req.GetFromAccountId(), req.GetToAccountId())
	return &accountpb.UpdateBalanceResponse{Success: true}, nil
}
