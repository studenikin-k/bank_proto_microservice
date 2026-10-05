package main

import (
	"context"
	"log/slog"
	"net"
	"time"

	grpcHandler "bank_proto_microservice/internal/auth/delivery/grpc"
	"bank_proto_microservice/internal/auth/repository"
	"bank_proto_microservice/internal/auth/service"
	"bank_proto_microservice/internal/utils"
	authpb "bank_proto_microservice/proto/auth"

	"golang.org/x/crypto/bcrypt"
)

func main() {
	utils.InitLogger("auth-service")

	grpcAddr := utils.Env("GRPC_ADDR", ":50051")
	utils.RunHealthcheckCommand(func() error { return utils.GRPCHealthcheck(grpcAddr) })

	dbCfg := utils.PostgresFromEnv("postgres://auth_user:auth_password@localhost:5431/auth_db?sslmode=disable")
	jwtSecret := utils.Env("JWT_SECRET", "dev-only-jwt-secret-change-me")
	jwtTTL := utils.EnvDuration("JWT_TTL", 24*time.Hour)
	bcryptCost := utils.EnvInt("BCRYPT_COST", bcrypt.DefaultCost)
	if bcryptCost < bcrypt.MinCost || bcryptCost > bcrypt.MaxCost {
		utils.Fatal("BCRYPT_COST вне допустимого диапазона 4..31", "value", bcryptCost)
	}

	startCtx, cancelStart := context.WithTimeout(context.Background(), time.Minute)
	defer cancelStart()

	pool, err := utils.ConnectPostgres(startCtx, dbCfg)
	if err != nil {
		utils.Fatal("не удалось подключиться к БД пользователей", "err", err)
	}
	defer pool.Close()
	utils.PublishPoolStats("users", pool)

	authSvc := service.NewAuthService(jwtSecret, jwtTTL, bcryptCost)
	server, health := utils.NewGRPCServer()
	authpb.RegisterAuthServiceServer(server, grpcHandler.NewAuthServer(authSvc, repository.NewUserRepository(pool)))

	listener, err := net.Listen("tcp", grpcAddr)
	if err != nil {
		utils.Fatal("не удалось открыть порт", "addr", grpcAddr, "err", err)
	}

	utils.StartDebugServer(utils.Env("DEBUG_ADDR", ":6061"))

	go func() {
		if err := server.Serve(listener); err != nil {
			utils.Fatal("gRPC сервер остановлен с ошибкой", "err", err)
		}
	}()
	slog.Info("Auth Service запущен", "grpc", grpcAddr, "db_max_conns", dbCfg.MaxConns,
		"jwt_ttl", jwtTTL.String(), "bcrypt_cost", bcryptCost)

	utils.WaitForShutdown()
	health.Shutdown()
	server.GracefulStop()
	slog.Info("Auth Service остановлен")
}
