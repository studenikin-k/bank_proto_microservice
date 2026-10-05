package grpc

import (
	"context"
	"log/slog"
	"time"

	"bank_proto_microservice/internal/apperr"
	"bank_proto_microservice/internal/money"
	"bank_proto_microservice/internal/transaction/models"
	"bank_proto_microservice/internal/transaction/service"
	transactionpb "bank_proto_microservice/proto/transaction"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

type TransactionServer struct {
	transactionpb.UnimplementedTransactionServiceServer
	svc *service.TransactionService
}

func NewTransactionServer(svc *service.TransactionService) *TransactionServer {
	return &TransactionServer{svc: svc}
}

func toProto(t *models.Transaction, replay bool) *transactionpb.TransactionResponse {
	return &transactionpb.TransactionResponse{
		Id:               t.ID,
		Type:             t.Type,
		FromAccountId:    t.FromAccountID,
		ToAccountId:      t.ToAccountID,
		Amount:           t.Amount.Kopecks(),
		FeePercent:       int32(t.FeePercent),
		FeeAmount:        t.FeeAmount.Kopecks(),
		TotalDebit:       t.TotalDebit.Kopecks(),
		Status:           t.Status,
		FailureReason:    t.FailureReason,
		CreatedAt:        t.CreatedAt.Format(time.RFC3339),
		UpdatedAt:        t.UpdatedAt.Format(time.RFC3339),
		IdempotentReplay: replay,
	}
}

func createRequest(req *transactionpb.CreateTransactionRequest) service.CreateRequest {
	return service.CreateRequest{
		UserID:         req.GetUserId(),
		IdempotencyKey: req.GetIdempotencyKey(),
		Type:           req.GetType(),
		FromAccountID:  req.GetFromAccountId(),
		ToAccountID:    req.GetToAccountId(),
		Amount:         money.Amount(req.GetAmount()),
	}
}

func (s *TransactionServer) CreateTransaction(ctx context.Context, req *transactionpb.CreateTransactionRequest) (*transactionpb.TransactionResponse, error) {
	t, replay, err := s.svc.Create(ctx, createRequest(req))
	if err != nil {
		return nil, toStatus(err)
	}
	return toProto(t, replay), nil
}

func (s *TransactionServer) CreateTransactionAsync(ctx context.Context, req *transactionpb.CreateTransactionRequest) (*transactionpb.TransactionResponse, error) {
	t, replay, err := s.svc.CreateAsync(ctx, createRequest(req))
	if err != nil {
		return nil, toStatus(err)
	}
	return toProto(t, replay), nil
}

func (s *TransactionServer) GetHistory(ctx context.Context, req *transactionpb.GetHistoryRequest) (*transactionpb.TransactionListResponse, error) {
	list, err := s.svc.History(ctx, req.GetUserId(), req.GetAccountId(), int(req.GetLimit()))
	if err != nil {
		return nil, toStatus(err)
	}
	resp := &transactionpb.TransactionListResponse{
		Transactions: make([]*transactionpb.TransactionResponse, 0, len(list)),
		Total:        int32(len(list)),
	}
	for i := range list {
		resp.Transactions = append(resp.Transactions, toProto(&list[i], false))
	}
	return resp, nil
}

func (s *TransactionServer) GetByID(ctx context.Context, req *transactionpb.GetByIDRequest) (*transactionpb.TransactionResponse, error) {
	t, err := s.svc.Get(ctx, req.GetUserId(), req.GetTransactionId())
	if err != nil {
		return nil, toStatus(err)
	}
	return toProto(t, false), nil
}

// toStatus пропускает gRPC-ошибки как есть (в том числе ошибки Account Service
// с их причинами), а прочие превращает во внутреннюю ошибку.
func toStatus(err error) error {
	if _, ok := status.FromError(err); ok {
		return err
	}
	if e := apperr.FromContext(err); e != nil {
		return e
	}
	slog.Error("внутренняя ошибка Transaction Service", "err", err)
	return apperr.New(codes.Internal, apperr.ReasonInternal, "внутренняя ошибка сервиса транзакций")
}
