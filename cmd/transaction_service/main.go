package main

import (
	"context"
	"net"
	"os"
	"os/signal"
	"syscall"
	"time"

	grpcHandler "bank_proto_microservice/internal/transaction/delivery/grpc"
	"bank_proto_microservice/internal/transaction/repository"
	"bank_proto_microservice/internal/transaction/service"
	"bank_proto_microservice/internal/transaction/worker"
	"bank_proto_microservice/internal/utils"
	accountpb "bank_proto_microservice/proto/account"
	transactionpb "bank_proto_microservice/proto/transaction"

	"github.com/jackc/pgx/v5/pgxpool"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
)

const (
	port           = ":50053"
	dbURL          = "postgres://tx_user:tx_password@localhost:5433/tx_db?sslmode=disable"
	accountGrpcURL = "localhost:50052" // Адрес Account Service
)

func main() {
	utils.LogInfo("TransactionService", "Запуск Transaction Service...")

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	// 1. Подключение к БД Transactions (порт 5433)
	pool, err := pgxpool.New(ctx, dbURL)
	if err != nil {
		utils.LogError("TransactionService", "Ошибка подключения к БД Transactions", err)
		os.Exit(1)
	}
	defer pool.Close()

	if err := pool.Ping(ctx); err != nil {
		utils.LogError("TransactionService", "БД Transactions недоступна (Ping failed)", err)
		os.Exit(1)
	}
	utils.LogSuccess("TransactionService", "Успешное подключение к PostgreSQL (Transactions DB на порту 5433)")

	// 2. Инициализация gRPC-клиента к Account Service
	utils.LogInfo("TransactionService", "Подключение gRPC к Account Service на "+accountGrpcURL)
	accountConn, err := grpc.Dial(accountGrpcURL, grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		utils.LogError("TransactionService", "Не удалось установить gRPC соединение с Account Service", err)
		os.Exit(1)
	}
	defer accountConn.Close()
	accountClient := accountpb.NewAccountServiceClient(accountConn)
	utils.LogSuccess("TransactionService", "gRPC-клиент к Account Service успешно инициализирован")

	// 3. Запуск Worker Pool (100 воркеров, буфер 1000, 3 ретрая)
	workerPool := worker.NewWorkerPool(100, 1000, 3)
	workerPool.Start()

	// 4. Слои бизнес-логики
	txRepo := repository.NewTransactionRepository(pool)
	txSvc := service.NewTransactionService(txRepo, accountClient, workerPool)
	grpcServer := grpcHandler.NewTransactionServer(txSvc)

	// 5. Запуск gRPC-сервера
	listener, err := net.Listen("tcp", port)
	if err != nil {
		utils.LogError("TransactionService", "Ошибка открытия TCP-порта "+port, err)
		os.Exit(1)
	}

	server := grpc.NewServer()
	transactionpb.RegisterTransactionServiceServer(server, grpcServer)

	go func() {
		stop := make(chan os.Signal, 1)
		signal.Notify(stop, os.Interrupt, syscall.SIGTERM)
		<-stop
		utils.LogWarning("TransactionService", "Остановка Transaction Service...")
		_ = workerPool.Shutdown(5 * time.Second)
		server.GracefulStop()
	}()

	utils.LogSuccess("TransactionService", "gRPC сервер запущен и ожидает соединений на "+port)
	if err := server.Serve(listener); err != nil {
		utils.LogError("TransactionService", "Ошибка работы gRPC сервера", err)
	}
}
