package middleware

import (
	"encoding/json"
	"strings"

	"bank_proto_microservice/internal/apperr"

	"github.com/golang-jwt/jwt/v4"
	"github.com/google/uuid"
	"github.com/valyala/fasthttp"
)

type Claims struct {
	UserID string `json:"user_id"`
	jwt.RegisteredClaims
}

// AuthMiddleware проверяет JWT, выданный Auth Service (общий секрет HS256),
// и кладёт user_id в контекст запроса. Шлюз проверяет только подлинность токена;
// права на конкретные счета проверяют сервисы, которые владеют данными.
type AuthMiddleware struct {
	secret []byte
	parser *jwt.Parser
}

func NewAuthMiddleware(secret string) *AuthMiddleware {
	return &AuthMiddleware{
		secret: []byte(secret),
		parser: jwt.NewParser(jwt.WithValidMethods([]string{jwt.SigningMethodHS256.Alg()})),
	}
}

func unauthorized(ctx *fasthttp.RequestCtx, msg string) {
	ctx.SetContentType("application/json; charset=utf-8")
	ctx.SetStatusCode(fasthttp.StatusUnauthorized)
	_ = json.NewEncoder(ctx).Encode(map[string]string{"error": msg, "code": apperr.ReasonUnauthenticated})
}

func (m *AuthMiddleware) RequireAuth(next fasthttp.RequestHandler) fasthttp.RequestHandler {
	return func(ctx *fasthttp.RequestCtx) {
		header := string(ctx.Request.Header.Peek("Authorization"))
		if header == "" {
			unauthorized(ctx, "требуется авторизация")
			return
		}
		tokenString, ok := strings.CutPrefix(header, "Bearer ")
		if !ok || tokenString == "" {
			unauthorized(ctx, "неверный формат заголовка Authorization, ожидается: Bearer <token>")
			return
		}

		claims := &Claims{}
		token, err := m.parser.ParseWithClaims(tokenString, claims, func(*jwt.Token) (any, error) {
			return m.secret, nil
		})
		if err != nil || !token.Valid || uuid.Validate(claims.UserID) != nil {
			unauthorized(ctx, "невалидный или истёкший токен")
			return
		}

		ctx.SetUserValue("user_id", claims.UserID)
		next(ctx)
	}
}
