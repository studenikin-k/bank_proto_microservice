package main

import (
	"context"
	"net"
	"os"
	"os/signal"
	"syscall"
	"time"

	grpcHandler "bank_proto_microservice/internal/auth/delivery/grpc"
	"bank_proto_microservice/internal/auth/repository"
	"bank_proto_microservice/internal/auth/service"
	"bank_proto_microservice/internal/utils"
	authpb "bank_proto_microservice/proto/auth"

	"github.com/jackc/pgx/v5/pgxpool"
	"google.golang.org/grpc"
)

const (
	port        = ":50051"
	dbURL       = "postgres://auth_user:auth_password@localhost:5431/auth_db?sslmode=disable"
	jwtSecret   = "super-secret-jwt-key-2026"
	jwtDuration = 24 * time.Hour
)

func main() {
	utils.LogInfo("AuthService", "Запуск Auth Service...")

	// 1. Подключение к БД Users (порт 5431)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	pool, err := pgxpool.New(ctx, dbURL)
	if err != nil {
		utils.LogError("AuthService", "Ошибка подключения к БД", err)
		os.Exit(1)
	}
	defer pool.Close()

	if err := pool.Ping(ctx); err != nil {
		utils.LogError("AuthService", "БД недоступна (Ping failed)", err)
		os.Exit(1)
	}
	utils.LogSuccess("AuthService", "Успешное подключение к PostgreSQL (Users DB на порту 5431)")

	// 2. Инициализация слоёв
	userRepo := repository.NewUserRepository(pool)
	authSvc := service.NewAuthService(jwtSecret, jwtDuration)
	authGrpcServer := grpcHandler.NewAuthServer(authSvc, userRepo)

	// 3. Запуск gRPC-сервера
	listener, err := net.Listen("tcp", port)
	if err != nil {
		utils.LogError("AuthService", "Ошибка открытия TCP-порта", err)
		os.Exit(1)
	}

	server := grpc.NewServer()
	authpb.RegisterAuthServiceServer(server, authGrpcServer)

	// Graceful shutdown в отдельной горутине
	go func() {
		stop := make(chan os.Signal, 1)
		signal.Notify(stop, os.Interrupt, syscall.SIGTERM)
		<-stop
		utils.LogWarning("AuthService", "Остановка gRPC сервера...")
		server.GracefulStop()
	}()

	utils.LogSuccess("AuthService", "gRPC сервер запущен и ожидает соединений на "+port)
	if err := server.Serve(listener); err != nil {
		utils.LogError("AuthService", "Ошибка работы gRPC сервера", err)
	}
}
