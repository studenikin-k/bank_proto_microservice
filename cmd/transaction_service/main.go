package main

import (
	"context"
	"log/slog"
	"net"
	"time"

	grpcHandler "bank_proto_microservice/internal/transaction/delivery/grpc"
	"bank_proto_microservice/internal/transaction/repository"
	"bank_proto_microservice/internal/transaction/service"
	"bank_proto_microservice/internal/transaction/worker"
	"bank_proto_microservice/internal/utils"
	accountpb "bank_proto_microservice/proto/account"
	transactionpb "bank_proto_microservice/proto/transaction"
)

func main() {
	utils.InitLogger("transaction-service")

	grpcAddr := utils.Env("GRPC_ADDR", ":50053")
	utils.RunHealthcheckCommand(func() error { return utils.GRPCHealthcheck(grpcAddr) })

	dbCfg := utils.PostgresFromEnv("postgres://tx_user:tx_password@localhost:5433/tx_db?sslmode=disable")
	accountAddr := utils.Env("ACCOUNT_SERVICE_ADDR", "localhost:50052")
	callTimeout := utils.EnvDuration("ACCOUNT_CALL_TIMEOUT", 3*time.Second)
	recoveryInterval := utils.EnvDuration("RECOVERY_INTERVAL", 5*time.Second)
	recoveryAge := utils.EnvDuration("RECOVERY_AGE", 30*time.Second)
	workers := utils.EnvInt("WORKER_COUNT", 100)
	queueSize := utils.EnvInt("WORKER_QUEUE_SIZE", 1000)
	retries := utils.EnvInt("WORKER_MAX_RETRIES", 3)

	// Recovery не должен трогать запись, пока её исход ещё может определить обычный вызов.
	if recoveryAge < 2*callTimeout {
		utils.Fatal("RECOVERY_AGE должен быть хотя бы вдвое больше ACCOUNT_CALL_TIMEOUT",
			"recovery_age", recoveryAge.String(), "account_call_timeout", callTimeout.String())
	}

	startCtx, cancelStart := context.WithTimeout(context.Background(), time.Minute)
	defer cancelStart()

	pool, err := utils.ConnectPostgres(startCtx, dbCfg)
	if err != nil {
		utils.Fatal("не удалось подключиться к БД транзакций", "err", err)
	}
	defer pool.Close()
	utils.PublishPoolStats("transactions", pool)

	accountConn, err := utils.DialGRPC(accountAddr)
	if err != nil {
		utils.Fatal("некорректный адрес Account Service", "addr", accountAddr, "err", err)
	}
	defer accountConn.Close()

	workerPool := worker.NewWorkerPool(workers, queueSize, retries)
	workerPool.Start()

	svc := service.NewTransactionService(
		repository.NewTransactionRepository(pool),
		accountpb.NewAccountServiceClient(accountConn),
		workerPool,
		service.Config{AccountCallTimeout: callTimeout},
	)

	server, health := utils.NewGRPCServer()
	transactionpb.RegisterTransactionServiceServer(server, grpcHandler.NewTransactionServer(svc))

	listener, err := net.Listen("tcp", grpcAddr)
	if err != nil {
		utils.Fatal("не удалось открыть порт", "addr", grpcAddr, "err", err)
	}

	utils.StartDebugServer(utils.Env("DEBUG_ADDR", ":6063"))

	bgCtx, stopBackground := context.WithCancel(context.Background())
	go svc.RunRecovery(bgCtx, recoveryInterval, recoveryAge)

	go func() {
		if err := server.Serve(listener); err != nil {
			utils.Fatal("gRPC сервер остановлен с ошибкой", "err", err)
		}
	}()
	slog.Info("Transaction Service запущен",
		"grpc", grpcAddr, "account_service", accountAddr, "db_max_conns", dbCfg.MaxConns,
		"account_call_timeout", callTimeout.String(), "recovery_age", recoveryAge.String(),
		"workers", workers, "queue", queueSize)

	utils.WaitForShutdown()
	health.Shutdown()
	server.GracefulStop()
	if err := workerPool.Shutdown(10 * time.Second); err != nil {
		slog.Warn("остановка воркеров", "err", err)
	}
	stopBackground()
	slog.Info("Transaction Service остановлен")
}
