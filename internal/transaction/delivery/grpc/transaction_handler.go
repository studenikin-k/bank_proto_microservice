package grpc

import (
	"context"
	"time"

	"bank_proto_microservice/internal/transaction/service"
	"bank_proto_microservice/internal/utils"
	transactionpb "bank_proto_microservice/proto/transaction"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

type TransactionServer struct {
	transactionpb.UnimplementedTransactionServiceServer
	svc *service.TransactionService
}

func NewTransactionServer(svc *service.TransactionService) *TransactionServer {
	utils.LogSuccess("TransactionServer", "Инициализирован gRPC сервер TransactionService")
	return &TransactionServer{svc: svc}
}

func (s *TransactionServer) Transfer(ctx context.Context, req *transactionpb.TransferRequest) (*transactionpb.TransactionResponse, error) {
	utils.LogInfo("TransactionServer", "📥 [gRPC Transfer] Входящий синхронный перевод: %s -> %s (%.2f)",
		req.GetFromAccountId(), req.GetToAccountId(), req.GetAmount())

	tx, err := s.svc.Transfer(ctx, req.GetUserId(), req.GetFromAccountId(), req.GetToAccountId(), req.GetAmount())
	if err != nil {
		utils.LogError("TransactionServer", "Ошибка при выполнении синхронного перевода", err)
		return nil, status.Errorf(codes.InvalidArgument, "Ошибка перевода: %v", err)
	}

	return &transactionpb.TransactionResponse{
		Id:            tx.ID,
		Type:          tx.Type,
		FromAccountId: tx.FromAccountID,
		ToAccountId:   tx.ToAccountID,
		Amount:        tx.Amount,
		FeePercent:    int32(tx.FeePercent),
		FeeAmount:     tx.FeeAmount,
		TotalDebit:    tx.TotalDebit,
		Status:        tx.Status,
		CreatedAt:     tx.CreatedAt.Format(time.RFC3339),
	}, nil
}

func (s *TransactionServer) Payment(ctx context.Context, req *transactionpb.PaymentRequest) (*transactionpb.TransactionResponse, error) {
	utils.LogInfo("TransactionServer", "📥 [gRPC Payment] Входящий платёж: %s -> %s (%.2f)",
		req.GetFromAccountId(), req.GetToAccountId(), req.GetAmount())

	tx, err := s.svc.Payment(ctx, req.GetUserId(), req.GetFromAccountId(), req.GetToAccountId(), req.GetAmount())
	if err != nil {
		utils.LogError("TransactionServer", "Ошибка при выполнении платежа", err)
		return nil, status.Errorf(codes.InvalidArgument, "Ошибка платежа: %v", err)
	}

	return &transactionpb.TransactionResponse{
		Id:            tx.ID,
		Type:          tx.Type,
		FromAccountId: tx.FromAccountID,
		ToAccountId:   tx.ToAccountID,
		Amount:        tx.Amount,
		FeePercent:    int32(tx.FeePercent),
		FeeAmount:     tx.FeeAmount,
		TotalDebit:    tx.TotalDebit,
		Status:        tx.Status,
		CreatedAt:     tx.CreatedAt.Format(time.RFC3339),
	}, nil
}

func (s *TransactionServer) TransferAsync(ctx context.Context, req *transactionpb.TransferRequest) (*transactionpb.AsyncResponse, error) {
	utils.LogInfo("TransactionServer", "📥 [gRPC TransferAsync] Входящий запрос на асинхронный перевод")

	taskID, err := s.svc.TransferAsync(req.GetUserId(), req.GetFromAccountId(), req.GetToAccountId(), req.GetAmount())
	if err != nil {
		utils.LogError("TransactionServer", "Ошибка постановки в очередь", err)
		return nil, status.Errorf(codes.ResourceExhausted, "Сервер перегружен: %v", err)
	}

	return &transactionpb.AsyncResponse{
		Status:  "accepted",
		Message: "Транзакция принята в обработку, TaskID: " + taskID,
	}, nil
}

func (s *TransactionServer) GetHistory(ctx context.Context, req *transactionpb.GetHistoryRequest) (*transactionpb.TransactionListResponse, error) {
	accountID := req.GetAccountId()
	utils.LogInfo("TransactionServer", "📥 [gRPC GetHistory] Запрос истории по счёту: %s", accountID)

	transactions, err := s.svc.GetHistory(ctx, accountID)
	if err != nil {
		utils.LogError("TransactionServer", "Ошибка получения истории", err)
		return nil, status.Errorf(codes.Internal, "Ошибка получения истории: %v", err)
	}

	var res []*transactionpb.TransactionResponse
	for _, tx := range transactions {
		res = append(res, &transactionpb.TransactionResponse{
			Id:            tx.ID,
			Type:          tx.Type,
			FromAccountId: tx.FromAccountID,
			ToAccountId:   tx.ToAccountID,
			Amount:        tx.Amount,
			FeePercent:    int32(tx.FeePercent),
			FeeAmount:     tx.FeeAmount,
			TotalDebit:    tx.TotalDebit,
			Status:        tx.Status,
			CreatedAt:     tx.CreatedAt.Format(time.RFC3339),
		})
	}

	return &transactionpb.TransactionListResponse{
		Transactions: res,
		Total:        int32(len(res)),
	}, nil
}

func (s *TransactionServer) GetByID(ctx context.Context, req *transactionpb.GetByIDRequest) (*transactionpb.TransactionResponse, error) {
	tx, err := s.svc.GetByID(ctx, req.GetTransactionId())
	if err != nil {
		return nil, status.Errorf(codes.NotFound, "Транзакция не найдена")
	}

	return &transactionpb.TransactionResponse{
		Id:            tx.ID,
		Type:          tx.Type,
		FromAccountId: tx.FromAccountID,
		ToAccountId:   tx.ToAccountID,
		Amount:        tx.Amount,
		FeePercent:    int32(tx.FeePercent),
		FeeAmount:     tx.FeeAmount,
		TotalDebit:    tx.TotalDebit,
		Status:        tx.Status,
		CreatedAt:     tx.CreatedAt.Format(time.RFC3339),
	}, nil
}
