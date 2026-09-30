package handlers

import (
	"encoding/json"
	"time"

	"bank_proto_microservice/internal/utils"
	accountpb "bank_proto_microservice/proto/account"
	authpb "bank_proto_microservice/proto/auth"
	transactionpb "bank_proto_microservice/proto/transaction"

	"github.com/valyala/fasthttp"
	"google.golang.org/grpc/status"
)

type GatewayHandler struct {
	authClient        authpb.AuthServiceClient
	accountClient     accountpb.AccountServiceClient
	transactionClient transactionpb.TransactionServiceClient
}

func NewGatewayHandler(
	authClient authpb.AuthServiceClient,
	accountClient accountpb.AccountServiceClient,
	transactionClient transactionpb.TransactionServiceClient,
) *GatewayHandler {
	utils.LogSuccess("GatewayHandler", "Инициализированы HTTP-обработчики шлюза")
	return &GatewayHandler{
		authClient:        authClient,
		accountClient:     accountClient,
		transactionClient: transactionClient,
	}
}

// ==================== AUTH ====================

func (h *GatewayHandler) Register(ctx *fasthttp.RequestCtx) {
	start := time.Now()
	utils.LogRequest("POST", "/register", "anonymous")

	var req struct {
		Name     string `json:"name"`
		Password string `json:"password"`
	}
	if err := json.Unmarshal(ctx.PostBody(), &req); err != nil {
		ctx.SetStatusCode(fasthttp.StatusBadRequest)
		_ = json.NewEncoder(ctx).Encode(map[string]string{"error": "Неверный формат данных"})
		return
	}

	resp, err := h.authClient.Register(ctx, &authpb.RegisterRequest{
		Name:     req.Name,
		Password: req.Password,
	})
	if err != nil {
		st, _ := status.FromError(err)
		ctx.SetStatusCode(fasthttp.StatusConflict)
		_ = json.NewEncoder(ctx).Encode(map[string]string{"error": st.Message()})
		return
	}

	ctx.SetStatusCode(fasthttp.StatusCreated)
	ctx.SetContentType("application/json")
	_ = json.NewEncoder(ctx).Encode(map[string]interface{}{
		"message":    "Пользователь успешно зарегистрирован",
		"user_id":    resp.GetUserId(),
		"name":       resp.GetName(),
		"created_at": resp.GetCreatedAt(),
	})
	utils.LogResponse("/register", fasthttp.StatusCreated, time.Since(start))
}

func (h *GatewayHandler) Login(ctx *fasthttp.RequestCtx) {
	start := time.Now()
	utils.LogRequest("POST", "/login", "anonymous")

	var req struct {
		Name     string `json:"name"`
		Password string `json:"password"`
	}
	if err := json.Unmarshal(ctx.PostBody(), &req); err != nil {
		ctx.SetStatusCode(fasthttp.StatusBadRequest)
		_ = json.NewEncoder(ctx).Encode(map[string]string{"error": "Неверный формат данных"})
		return
	}

	resp, err := h.authClient.Login(ctx, &authpb.LoginRequest{
		Name:     req.Name,
		Password: req.Password,
	})
	if err != nil {
		st, _ := status.FromError(err)
		ctx.SetStatusCode(fasthttp.StatusUnauthorized)
		_ = json.NewEncoder(ctx).Encode(map[string]string{"error": st.Message()})
		return
	}

	ctx.SetStatusCode(fasthttp.StatusOK)
	ctx.SetContentType("application/json")
	_ = json.NewEncoder(ctx).Encode(map[string]interface{}{
		"message":    "Вход выполнен успешно",
		"token":      resp.GetToken(),
		"user_id":    resp.GetUserId(),
		"name":       resp.GetName(),
		"expires_in": resp.GetExpiresIn(),
	})
	utils.LogResponse("/login", fasthttp.StatusOK, time.Since(start))
}

func (h *GatewayHandler) DeleteUser(ctx *fasthttp.RequestCtx) {
	userID := ctx.UserValue("user_id").(string)
	utils.LogRequest("DELETE", "/users/me", userID)

	resp, err := h.authClient.DeleteUser(ctx, &authpb.DeleteUserRequest{UserId: userID})
	if err != nil {
		st, _ := status.FromError(err)
		ctx.SetStatusCode(fasthttp.StatusInternalServerError)
		_ = json.NewEncoder(ctx).Encode(map[string]string{"error": st.Message()})
		return
	}

	ctx.SetStatusCode(fasthttp.StatusOK)
	ctx.SetContentType("application/json")
	_ = json.NewEncoder(ctx).Encode(map[string]interface{}{
		"message": resp.GetMessage(),
		"user_id": userID,
	})
}

// ==================== ACCOUNTS ====================

func (h *GatewayHandler) CreateAccount(ctx *fasthttp.RequestCtx) {
	userID := ctx.UserValue("user_id").(string)
	utils.LogRequest("POST", "/accounts", userID)

	resp, err := h.accountClient.CreateAccount(ctx, &accountpb.CreateAccountRequest{UserId: userID})
	if err != nil {
		st, _ := status.FromError(err)
		ctx.SetStatusCode(fasthttp.StatusForbidden)
		_ = json.NewEncoder(ctx).Encode(map[string]string{"error": st.Message()})
		return
	}

	ctx.SetStatusCode(fasthttp.StatusCreated)
	ctx.SetContentType("application/json")
	// ВАЖНО: отдаем и "id", и "account_id" для 100% совместимости с k6 тестами!
	_ = json.NewEncoder(ctx).Encode(map[string]interface{}{
		"id":         resp.GetId(),
		"account_id": resp.GetId(),
		"balance":    resp.GetBalance(),
		"status":     resp.GetStatus(),
		"created_at": resp.GetCreatedAt(),
	})
}

func (h *GatewayHandler) GetAccounts(ctx *fasthttp.RequestCtx) {
	userID := ctx.UserValue("user_id").(string)
	utils.LogRequest("GET", "/accounts", userID)

	resp, err := h.accountClient.GetUserAccounts(ctx, &accountpb.GetUserAccountsRequest{UserId: userID})
	if err != nil {
		st, _ := status.FromError(err)
		ctx.SetStatusCode(fasthttp.StatusInternalServerError)
		_ = json.NewEncoder(ctx).Encode(map[string]string{"error": st.Message()})
		return
	}

	var accounts []map[string]interface{}
	for _, a := range resp.GetAccounts() {
		accounts = append(accounts, map[string]interface{}{
			"id":         a.GetId(),
			"account_id": a.GetId(),
			"balance":    a.GetBalance(),
			"status":     a.GetStatus(),
			"created_at": a.GetCreatedAt(),
		})
	}

	ctx.SetStatusCode(fasthttp.StatusOK)
	ctx.SetContentType("application/json")
	_ = json.NewEncoder(ctx).Encode(map[string]interface{}{
		"accounts":        accounts,
		"total":           resp.GetTotal(),
		"active_count":    resp.GetActiveCount(),
		"closed_count":    resp.GetClosedCount(),
		"max_accounts":    resp.GetMaxAccounts(),
		"can_create_more": resp.GetCanCreateMore(),
	})
}

func (h *GatewayHandler) GetAccountByID(ctx *fasthttp.RequestCtx) {
	userID := ctx.UserValue("user_id").(string)
	accountID := ctx.UserValue("id").(string)
	utils.LogRequest("GET", "/accounts/"+accountID, userID)

	resp, err := h.accountClient.GetAccount(ctx, &accountpb.GetAccountRequest{
		AccountId: accountID,
		UserId:    userID,
	})
	if err != nil {
		st, _ := status.FromError(err)
		ctx.SetStatusCode(fasthttp.StatusNotFound)
		_ = json.NewEncoder(ctx).Encode(map[string]string{"error": st.Message()})
		return
	}

	ctx.SetStatusCode(fasthttp.StatusOK)
	ctx.SetContentType("application/json")
	_ = json.NewEncoder(ctx).Encode(map[string]interface{}{
		"id":         resp.GetId(),
		"account_id": resp.GetId(),
		"balance":    resp.GetBalance(),
		"status":     resp.GetStatus(),
		"created_at": resp.GetCreatedAt(),
	})
}

func (h *GatewayHandler) DeleteAccount(ctx *fasthttp.RequestCtx) {
	userID := ctx.UserValue("user_id").(string)
	accountID := ctx.UserValue("id").(string)
	utils.LogRequest("DELETE", "/accounts/"+accountID, userID)

	resp, err := h.accountClient.DeleteAccount(ctx, &accountpb.DeleteAccountRequest{
		AccountId: accountID,
		UserId:    userID,
	})
	if err != nil {
		st, _ := status.FromError(err)
		ctx.SetStatusCode(fasthttp.StatusBadRequest)
		_ = json.NewEncoder(ctx).Encode(map[string]string{"error": st.Message()})
		return
	}

	ctx.SetStatusCode(fasthttp.StatusOK)
	ctx.SetContentType("application/json")
	_ = json.NewEncoder(ctx).Encode(map[string]string{
		"message":    resp.GetMessage(),
		"account_id": resp.GetAccountId(),
	})
}

// ==================== TRANSACTIONS ====================

// ProcessTransaction — универсальный обработчик для POST /transactions из тестов k6
func (h *GatewayHandler) ProcessTransaction(ctx *fasthttp.RequestCtx) {
	userID := ctx.UserValue("user_id").(string)
	utils.LogRequest("POST", "/transactions", userID)

	var req struct {
		Type          string  `json:"type"` // "transfer" или "payment"
		FromAccountID string  `json:"from_account_id"`
		ToAccountID   string  `json:"to_account_id"`
		Amount        float64 `json:"amount"`
	}
	if err := json.Unmarshal(ctx.PostBody(), &req); err != nil {
		ctx.SetStatusCode(fasthttp.StatusBadRequest)
		_ = json.NewEncoder(ctx).Encode(map[string]string{"error": "invalid request body"})
		return
	}

	if req.Type == "payment" {
		resp, err := h.transactionClient.Payment(ctx, &transactionpb.PaymentRequest{
			UserId:        userID,
			FromAccountId: req.FromAccountID,
			ToAccountId:   req.ToAccountID,
			Amount:        req.Amount,
		})
		if err != nil {
			st, _ := status.FromError(err)
			ctx.SetStatusCode(fasthttp.StatusBadRequest)
			_ = json.NewEncoder(ctx).Encode(map[string]string{"error": st.Message()})
			return
		}
		ctx.SetStatusCode(fasthttp.StatusCreated)
		ctx.SetContentType("application/json")
		_ = json.NewEncoder(ctx).Encode(map[string]interface{}{
			"message":     "payment successful",
			"transaction": resp,
		})
		return
	}

	// По умолчанию transfer
	resp, err := h.transactionClient.Transfer(ctx, &transactionpb.TransferRequest{
		UserId:        userID,
		FromAccountId: req.FromAccountID,
		ToAccountId:   req.ToAccountID,
		Amount:        req.Amount,
	})
	if err != nil {
		st, _ := status.FromError(err)
		ctx.SetStatusCode(fasthttp.StatusBadRequest)
		_ = json.NewEncoder(ctx).Encode(map[string]string{"error": st.Message()})
		return
	}

	ctx.SetStatusCode(fasthttp.StatusCreated)
	ctx.SetContentType("application/json")
	_ = json.NewEncoder(ctx).Encode(map[string]interface{}{
		"message":     "transfer successful",
		"transaction": resp,
	})
}

func (h *GatewayHandler) Transfer(ctx *fasthttp.RequestCtx) {
	h.ProcessTransaction(ctx)
}

func (h *GatewayHandler) Payment(ctx *fasthttp.RequestCtx) {
	h.ProcessTransaction(ctx)
}

func (h *GatewayHandler) TransferAsync(ctx *fasthttp.RequestCtx) {
	userID := ctx.UserValue("user_id").(string)
	utils.LogRequest("POST", "/transactions/async", userID)

	var req struct {
		FromAccountID string  `json:"from_account_id"`
		ToAccountID   string  `json:"to_account_id"`
		Amount        float64 `json:"amount"`
	}
	if err := json.Unmarshal(ctx.PostBody(), &req); err != nil {
		ctx.SetStatusCode(fasthttp.StatusBadRequest)
		_ = json.NewEncoder(ctx).Encode(map[string]string{"error": "invalid request body"})
		return
	}

	resp, err := h.transactionClient.TransferAsync(ctx, &transactionpb.TransferRequest{
		UserId:        userID,
		FromAccountId: req.FromAccountID,
		ToAccountId:   req.ToAccountID,
		Amount:        req.Amount,
	})
	if err != nil {
		st, _ := status.FromError(err)
		ctx.SetStatusCode(fasthttp.StatusServiceUnavailable)
		_ = json.NewEncoder(ctx).Encode(map[string]string{"error": st.Message()})
		return
	}

	ctx.SetStatusCode(fasthttp.StatusAccepted)
	ctx.SetContentType("application/json")
	_ = json.NewEncoder(ctx).Encode(map[string]string{
		"status":  resp.GetStatus(),
		"message": resp.GetMessage(),
	})
}

func (h *GatewayHandler) GetHistory(ctx *fasthttp.RequestCtx) {
	userID := ctx.UserValue("user_id").(string)
	utils.LogRequest("GET", "/transactions", userID)

	accountIDBytes := ctx.QueryArgs().Peek("account_id")
	var accountIDPtr *string
	if len(accountIDBytes) > 0 {
		str := string(accountIDBytes)
		accountIDPtr = &str
	}

	resp, err := h.transactionClient.GetHistory(ctx, &transactionpb.GetHistoryRequest{
		UserId:    userID,
		AccountId: accountIDPtr,
	})
	if err != nil {
		st, _ := status.FromError(err)
		ctx.SetStatusCode(fasthttp.StatusInternalServerError)
		_ = json.NewEncoder(ctx).Encode(map[string]string{"error": st.Message()})
		return
	}

	ctx.SetStatusCode(fasthttp.StatusOK)
	ctx.SetContentType("application/json")
	_ = json.NewEncoder(ctx).Encode(map[string]interface{}{
		"transactions": resp.GetTransactions(),
		"total":        resp.GetTotal(),
	})
}

func (h *GatewayHandler) GetTransactionByID(ctx *fasthttp.RequestCtx) {
	userID := ctx.UserValue("user_id").(string)
	txID := ctx.UserValue("id").(string)
	utils.LogRequest("GET", "/transactions/"+txID, userID)

	resp, err := h.transactionClient.GetByID(ctx, &transactionpb.GetByIDRequest{
		UserId:        userID,
		TransactionId: txID,
	})
	if err != nil {
		st, _ := status.FromError(err)
		ctx.SetStatusCode(fasthttp.StatusNotFound)
		_ = json.NewEncoder(ctx).Encode(map[string]string{"error": st.Message()})
		return
	}

	ctx.SetStatusCode(fasthttp.StatusOK)
	ctx.SetContentType("application/json")
	_ = json.NewEncoder(ctx).Encode(resp)
}
