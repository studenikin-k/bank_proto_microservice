package main

import (
	"encoding/json"
	"os"
	"time"

	"bank_proto_microservice/internal/gateway/handlers"
	"bank_proto_microservice/internal/gateway/middleware"
	"bank_proto_microservice/internal/utils"
	accountpb "bank_proto_microservice/proto/account"
	authpb "bank_proto_microservice/proto/auth"
	transactionpb "bank_proto_microservice/proto/transaction"

	"github.com/valyala/fasthttp"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
)

const (
	httpPort           = ":8080"
	jwtSecret          = "super-secret-jwt-key-2026"
	authServiceAddr    = "localhost:50051"
	accountServiceAddr = "localhost:50052"
	txServiceAddr      = "localhost:50053"
)

func main() {
	utils.LogInfo("Gateway", "Запуск API Gateway (Fasthttp -> gRPC)...")

	// 1. Инициализация gRPC-клиентов
	authConn, err := grpc.Dial(authServiceAddr, grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		utils.LogError("Gateway", "Ошибка подключения к Auth Service", err)
		os.Exit(1)
	}
	defer authConn.Close()
	authClient := authpb.NewAuthServiceClient(authConn)
	utils.LogSuccess("Gateway", "Подключено к Auth Service (%s)", authServiceAddr)

	accountConn, err := grpc.Dial(accountServiceAddr, grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		utils.LogError("Gateway", "Ошибка подключения к Account Service", err)
		os.Exit(1)
	}
	defer accountConn.Close()
	accountClient := accountpb.NewAccountServiceClient(accountConn)
	utils.LogSuccess("Gateway", "Подключено к Account Service (%s)", accountServiceAddr)

	txConn, err := grpc.Dial(txServiceAddr, grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		utils.LogError("Gateway", "Ошибка подключения к Transaction Service", err)
		os.Exit(1)
	}
	defer txConn.Close()
	txClient := transactionpb.NewTransactionServiceClient(txConn)
	utils.LogSuccess("Gateway", "Подключено к Transaction Service (%s)", txServiceAddr)

	// 2. Слои Gateway
	authMiddleware := middleware.NewAuthMiddleware(jwtSecret)
	gatewayHandler := handlers.NewGatewayHandler(authClient, accountClient, txClient)

	// 3. Роутер Fasthttp (100% совместимость со всеми тестами k6!)
	router := func(ctx *fasthttp.RequestCtx) {
		path := string(ctx.Path())
		method := string(ctx.Method())

		switch {
		// Health check
		case method == "GET" && path == "/health":
			ctx.SetContentType("application/json")
			ctx.SetStatusCode(fasthttp.StatusOK)
			_ = json.NewEncoder(ctx).Encode(map[string]interface{}{
				"status":  "OK",
				"time":    time.Now().Format(time.RFC1123),
				"message": "Bank Microservices API Gateway is running",
			})

		// Публичные маршруты Auth
		case method == "POST" && path == "/register":
			gatewayHandler.Register(ctx)

		case method == "POST" && path == "/login":
			gatewayHandler.Login(ctx)

		// Защищённые маршруты Auth
		case method == "DELETE" && path == "/users/me":
			authMiddleware.RequireAuth(gatewayHandler.DeleteUser)(ctx)

		// Счета (Accounts)
		case method == "POST" && path == "/accounts":
			authMiddleware.RequireAuth(gatewayHandler.CreateAccount)(ctx)

		case method == "GET" && path == "/accounts":
			authMiddleware.RequireAuth(gatewayHandler.GetAccounts)(ctx)

		case method == "GET" && len(path) > 10 && path[:10] == "/accounts/":
			accountID := path[10:]
			ctx.SetUserValue("id", accountID)
			authMiddleware.RequireAuth(gatewayHandler.GetAccountByID)(ctx)

		case method == "DELETE" && len(path) > 10 && path[:10] == "/accounts/":
			accountID := path[10:]
			ctx.SetUserValue("id", accountID)
			authMiddleware.RequireAuth(gatewayHandler.DeleteAccount)(ctx)

		// Точечные маршруты транзакций
		case method == "POST" && path == "/transactions/transfer":
			authMiddleware.RequireAuth(gatewayHandler.Transfer)(ctx)

		case method == "POST" && path == "/transactions/payment":
			authMiddleware.RequireAuth(gatewayHandler.Payment)(ctx)

		case method == "POST" && path == "/transactions/async":
			authMiddleware.RequireAuth(gatewayHandler.TransferAsync)(ctx)

		// Универсальный маршрут транзакций (для сценариев k6!)
		case method == "POST" && path == "/transactions":
			authMiddleware.RequireAuth(gatewayHandler.ProcessTransaction)(ctx)

		// История и получение по ID
		case method == "GET" && path == "/transactions":
			authMiddleware.RequireAuth(gatewayHandler.GetHistory)(ctx)

		case method == "GET" && len(path) > 14 && path[:14] == "/transactions/":
			txID := path[14:]
			ctx.SetUserValue("id", txID)
			authMiddleware.RequireAuth(gatewayHandler.GetTransactionByID)(ctx)

		default:
			utils.LogWarning("Router", "Неизвестный маршрут: %s %s", method, path)
			ctx.SetStatusCode(fasthttp.StatusNotFound)
			ctx.SetContentType("application/json")
			_ = json.NewEncoder(ctx).Encode(map[string]string{"error": "Маршрут не найден"})
		}
	}

	utils.LogSuccess("Gateway", "HTTP сервер API Gateway запущен на порту %s", httpPort)
	if err := fasthttp.ListenAndServe(httpPort, router); err != nil {
		utils.LogError("Gateway", "Ошибка запуска HTTP сервера", err)
	}
}
