package main

import (
	"context"
	"net"
	"os"
	"os/signal"
	"syscall"
	"time"

	"bank_proto_microservice/internal/account/cache"
	grpcHandler "bank_proto_microservice/internal/account/delivery/grpc"
	"bank_proto_microservice/internal/account/repository"
	"bank_proto_microservice/internal/account/service"
	"bank_proto_microservice/internal/utils"
	accountpb "bank_proto_microservice/proto/account"

	"github.com/jackc/pgx/v5/pgxpool"
	"google.golang.org/grpc"
)

const (
	port      = ":50052"
	dbURL     = "postgres://account_user:account_password@localhost:5432/account_db?sslmode=disable"
	redisAddr = "localhost:6379"
)

func main() {
	utils.LogInfo("AccountService", "Запуск Account Service...")

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	// 1. БД Accounts (порт 5432)
	pool, err := pgxpool.New(ctx, dbURL)
	if err != nil {
		utils.LogError("AccountService", "Ошибка подключения к БД Accounts", err)
		os.Exit(1)
	}
	defer pool.Close()

	if err := pool.Ping(ctx); err != nil {
		utils.LogError("AccountService", "БД Accounts недоступна", err)
		os.Exit(1)
	}
	utils.LogSuccess("AccountService", "Успешное подключение к PostgreSQL (Accounts DB на порту 5432)")

	// 2. Redis
	redisCache := cache.NewRedisCache(redisAddr)
	if err := redisCache.Ping(ctx); err != nil {
		utils.LogWarning("AccountService", "Redis недоступен, работа продолжается без кэша")
	} else {
		utils.LogSuccess("AccountService", "Успешное подключение к Redis (порт 6379)")
	}
	defer redisCache.Close()

	// 3. Слои
	repo := repository.NewAccountRepository(pool)
	svc := service.NewAccountService(repo, redisCache)
	grpcServer := grpcHandler.NewAccountServer(svc)

	// 4. gRPC
	listener, err := net.Listen("tcp", port)
	if err != nil {
		utils.LogError("AccountService", "Ошибка открытия TCP-порта", err)
		os.Exit(1)
	}

	server := grpc.NewServer()
	accountpb.RegisterAccountServiceServer(server, grpcServer)

	go func() {
		stop := make(chan os.Signal, 1)
		signal.Notify(stop, os.Interrupt, syscall.SIGTERM)
		<-stop
		utils.LogWarning("AccountService", "Остановка Account Service...")
		server.GracefulStop()
	}()

	utils.LogSuccess("AccountService", "gRPC сервер запущен и ожидает соединений на "+port)
	if err := server.Serve(listener); err != nil {
		utils.LogError("AccountService", "Ошибка работы gRPC сервера", err)
	}
}
