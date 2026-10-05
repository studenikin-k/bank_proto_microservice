package grpc

import (
	"context"
	"errors"
	"log/slog"
	"strings"
	"time"

	"bank_proto_microservice/internal/account/models"
	"bank_proto_microservice/internal/account/repository"
	"bank_proto_microservice/internal/account/service"
	"bank_proto_microservice/internal/apperr"
	"bank_proto_microservice/internal/money"
	accountpb "bank_proto_microservice/proto/account"

	"google.golang.org/grpc/codes"
)

type AccountServer struct {
	accountpb.UnimplementedAccountServiceServer
	svc *service.AccountService
}

func NewAccountServer(svc *service.AccountService) *AccountServer {
	return &AccountServer{svc: svc}
}

func toProto(a *models.Account) *accountpb.AccountResponse {
	return &accountpb.AccountResponse{
		Id:        a.ID,
		UserId:    a.UserID,
		Balance:   a.Balance.Kopecks(),
		Status:    a.Status,
		CreatedAt: a.CreatedAt.Format(time.RFC3339),
	}
}

func (s *AccountServer) CreateAccount(ctx context.Context, req *accountpb.CreateAccountRequest) (*accountpb.AccountResponse, error) {
	acc, err := s.svc.CreateAccount(ctx, req.GetUserId())
	if err != nil {
		return nil, toStatus(err)
	}
	return toProto(acc), nil
}

func (s *AccountServer) GetAccount(ctx context.Context, req *accountpb.GetAccountRequest) (*accountpb.AccountResponse, error) {
	acc, err := s.svc.GetAccount(ctx, req.GetAccountId(), req.GetUserId())
	if err != nil {
		return nil, toStatus(err)
	}
	return toProto(acc), nil
}

func (s *AccountServer) GetUserAccounts(ctx context.Context, req *accountpb.GetUserAccountsRequest) (*accountpb.AccountListResponse, error) {
	accounts, err := s.svc.GetUserAccounts(ctx, req.GetUserId())
	if err != nil {
		return nil, toStatus(err)
	}

	resp := &accountpb.AccountListResponse{
		Accounts:    make([]*accountpb.AccountResponse, 0, len(accounts)),
		MaxAccounts: service.MaxActiveAccounts,
	}
	for i := range accounts {
		resp.Accounts = append(resp.Accounts, toProto(&accounts[i]))
		if accounts[i].Status == models.StatusActive {
			resp.ActiveCount++
		} else {
			resp.ClosedCount++
		}
	}
	resp.Total = int32(len(accounts))
	resp.CanCreateMore = resp.ActiveCount < service.MaxActiveAccounts
	return resp, nil
}

func (s *AccountServer) DeleteAccount(ctx context.Context, req *accountpb.DeleteAccountRequest) (*accountpb.DeleteAccountResponse, error) {
	if err := s.svc.CloseAccount(ctx, req.GetAccountId(), req.GetUserId()); err != nil {
		return nil, toStatus(err)
	}
	return &accountpb.DeleteAccountResponse{
		Success:   true,
		Message:   "Счёт успешно закрыт",
		AccountId: req.GetAccountId(),
	}, nil
}

func (s *AccountServer) ApplyTransfer(ctx context.Context, req *accountpb.ApplyTransferRequest) (*accountpb.ApplyTransferResponse, error) {
	res, err := s.svc.ApplyTransfer(ctx, models.Transfer{
		ID:     req.GetTransferId(),
		UserID: req.GetUserId(),
		FromID: req.GetFromAccountId(),
		ToID:   req.GetToAccountId(),
		Amount: money.Amount(req.GetAmount()),
		Fee:    money.Amount(req.GetFee()),
	})
	if err != nil {
		return nil, toStatus(err)
	}
	return &accountpb.ApplyTransferResponse{AlreadyApplied: res.AlreadyApplied}, nil
}

func (s *AccountServer) ResolveTransfer(ctx context.Context, req *accountpb.ResolveTransferRequest) (*accountpb.ResolveTransferResponse, error) {
	applied, err := s.svc.ResolveTransfer(ctx, req.GetTransferId())
	if err != nil {
		return nil, toStatus(err)
	}
	outcome := accountpb.ResolveTransferResponse_ABORTED
	if applied {
		outcome = accountpb.ResolveTransferResponse_APPLIED
	}
	return &accountpb.ResolveTransferResponse{Outcome: outcome}, nil
}

// toStatus переводит доменные ошибки в gRPC-коды с машиночитаемой причиной.
func toStatus(err error) error {
	if e := apperr.FromContext(err); e != nil {
		return e
	}
	type mapping struct {
		target error
		code   codes.Code
		reason string
	}
	for _, m := range []mapping{
		{service.ErrInvalid, codes.InvalidArgument, apperr.ReasonInvalidArgument},
		{repository.ErrAccountNotFound, codes.NotFound, apperr.ReasonAccountNotFound},
		{repository.ErrRecipientNotFound, codes.NotFound, apperr.ReasonRecipientNotFound},
		{repository.ErrForbidden, codes.PermissionDenied, apperr.ReasonAccountForbidden},
		{repository.ErrAccountClosed, codes.FailedPrecondition, apperr.ReasonAccountClosed},
		{repository.ErrAlreadyClosed, codes.FailedPrecondition, apperr.ReasonAccountClosed},
		{repository.ErrRecipientClosed, codes.FailedPrecondition, apperr.ReasonRecipientClosed},
		{repository.ErrInsufficientFunds, codes.FailedPrecondition, apperr.ReasonInsufficientFunds},
		{repository.ErrAccountLimitReached, codes.FailedPrecondition, apperr.ReasonAccountLimit},
		{repository.ErrTransferAborted, codes.FailedPrecondition, apperr.ReasonTransferAborted},
		{repository.ErrTransferMismatch, codes.AlreadyExists, apperr.ReasonIdempotencyConflict},
		{repository.ErrConcurrency, codes.Aborted, apperr.ReasonConcurrencyConflict},
	} {
		if errors.Is(err, m.target) {
			return apperr.New(m.code, m.reason, publicMessage(err, m.target))
		}
	}
	slog.Error("внутренняя ошибка Account Service", "err", err)
	return apperr.New(codes.Internal, apperr.ReasonInternal, "внутренняя ошибка сервиса счетов")
}

// publicMessage отдаёт текст доменной ошибки без технических подробностей (текста ошибки PostgreSQL).
func publicMessage(err, target error) string {
	if errors.Is(target, service.ErrInvalid) {
		return strings.TrimPrefix(err.Error(), service.ErrInvalid.Error()+": ")
	}
	return target.Error()
}
