package main

import (
	"context"
	"log/slog"
	"net"
	"time"

	"bank_proto_microservice/internal/account/cache"
	grpcHandler "bank_proto_microservice/internal/account/delivery/grpc"
	"bank_proto_microservice/internal/account/repository"
	"bank_proto_microservice/internal/account/service"
	"bank_proto_microservice/internal/money"
	"bank_proto_microservice/internal/utils"
	accountpb "bank_proto_microservice/proto/account"
)

func main() {
	utils.InitLogger("account-service")

	grpcAddr := utils.Env("GRPC_ADDR", ":50052")
	utils.RunHealthcheckCommand(func() error { return utils.GRPCHealthcheck(grpcAddr) })

	dbCfg := utils.PostgresFromEnv("postgres://account_user:account_password@localhost:5432/account_db?sslmode=disable")
	redisAddr := utils.Env("REDIS_ADDR", "localhost:6379")
	cacheTTL := utils.EnvDuration("CACHE_TTL", 30*time.Second) // 0 — кеш выключен
	sweepInterval := utils.EnvDuration("FEE_SWEEP_INTERVAL", 5*time.Second)
	openingBalance, err := money.Parse(utils.Env("ACCOUNT_OPENING_BALANCE", "20000.00"))
	if err != nil || openingBalance < 0 {
		utils.Fatal("некорректный ACCOUNT_OPENING_BALANCE", "err", err)
	}

	startCtx, cancelStart := context.WithTimeout(context.Background(), time.Minute)
	defer cancelStart()

	pool, err := utils.ConnectPostgres(startCtx, dbCfg)
	if err != nil {
		utils.Fatal("не удалось подключиться к БД счетов", "err", err)
	}
	defer pool.Close()
	utils.PublishPoolStats("accounts", pool)

	var redisCache *cache.RedisCache
	if cacheTTL > 0 {
		redisCache = cache.NewRedisCache(redisAddr, cacheTTL)
		defer redisCache.Close()
		if err := redisCache.Ping(startCtx); err != nil {
			slog.Warn("Redis недоступен: запросы будут идти в БД, пока он не появится", "addr", redisAddr, "err", err)
		}
	}

	svc := service.NewAccountService(repository.NewAccountRepository(pool), redisCache, openingBalance)
	server, health := utils.NewGRPCServer()
	accountpb.RegisterAccountServiceServer(server, grpcHandler.NewAccountServer(svc))

	listener, err := net.Listen("tcp", grpcAddr)
	if err != nil {
		utils.Fatal("не удалось открыть порт", "addr", grpcAddr, "err", err)
	}

	utils.StartDebugServer(utils.Env("DEBUG_ADDR", ":6062"))

	bgCtx, stopBackground := context.WithCancel(context.Background())
	go svc.RunFeeSweeper(bgCtx, sweepInterval)

	go func() {
		if err := server.Serve(listener); err != nil {
			utils.Fatal("gRPC сервер остановлен с ошибкой", "err", err)
		}
	}()
	slog.Info("Account Service запущен",
		"grpc", grpcAddr, "db_max_conns", dbCfg.MaxConns, "cache_ttl", cacheTTL.String(),
		"opening_balance", openingBalance.String(), "fee_sweep_interval", sweepInterval.String())

	utils.WaitForShutdown()
	health.Shutdown()
	stopBackground()
	server.GracefulStop()
	slog.Info("Account Service остановлен")
}
