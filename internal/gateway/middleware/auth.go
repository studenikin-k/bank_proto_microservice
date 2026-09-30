package middleware

import (
	"encoding/json"
	"errors"
	"strings"
	"time"

	"bank_proto_microservice/internal/utils"

	"github.com/golang-jwt/jwt/v4"
	"github.com/valyala/fasthttp"
)

type Claims struct {
	UserID string `json:"user_id"`
	jwt.RegisteredClaims
}

type AuthMiddleware struct {
	jwtSecret string
}

func NewAuthMiddleware(secret string) *AuthMiddleware {
	utils.LogSuccess("Middleware", "Инициализирован middleware авторизации JWT")
	return &AuthMiddleware{
		jwtSecret: secret,
	}
}

func (m *AuthMiddleware) RequireAuth(next fasthttp.RequestHandler) fasthttp.RequestHandler {
	return func(ctx *fasthttp.RequestCtx) {
		startTime := time.Now()

		authHeader := string(ctx.Request.Header.Peek("Authorization"))
		if authHeader == "" {
			utils.LogWarning("Middleware", "Отсутствует заголовок Authorization")
			ctx.SetStatusCode(fasthttp.StatusUnauthorized)
			ctx.SetContentType("application/json")
			_ = json.NewEncoder(ctx).Encode(map[string]string{"error": "Требуется авторизация"})
			utils.LogResponse("RequireAuth", fasthttp.StatusUnauthorized, time.Since(startTime))
			return
		}

		parts := strings.Split(authHeader, " ")
		if len(parts) != 2 || parts[0] != "Bearer" {
			utils.LogWarning("Middleware", "Неверный формат заголовка Authorization")
			ctx.SetStatusCode(fasthttp.StatusUnauthorized)
			ctx.SetContentType("application/json")
			_ = json.NewEncoder(ctx).Encode(map[string]string{"error": "Неверный формат токена"})
			utils.LogResponse("RequireAuth", fasthttp.StatusUnauthorized, time.Since(startTime))
			return
		}

		tokenString := parts[1]

		token, err := jwt.ParseWithClaims(tokenString, &Claims{}, func(token *jwt.Token) (interface{}, error) {
			if _, ok := token.Method.(*jwt.SigningMethodHMAC); !ok {
				return nil, errors.New("unexpected signing method")
			}
			return []byte(m.jwtSecret), nil
		})

		if err != nil {
			utils.LogWarning("Middleware", "Невалидный токен: %v", err)
			ctx.SetStatusCode(fasthttp.StatusUnauthorized)
			ctx.SetContentType("application/json")
			_ = json.NewEncoder(ctx).Encode(map[string]string{"error": "Невалидный или истёкший токен"})
			utils.LogResponse("RequireAuth", fasthttp.StatusUnauthorized, time.Since(startTime))
			return
		}

		claims, ok := token.Claims.(*Claims)
		if !ok || !token.Valid {
			utils.LogWarning("Middleware", "Токен не прошёл валидацию")
			ctx.SetStatusCode(fasthttp.StatusUnauthorized)
			ctx.SetContentType("application/json")
			_ = json.NewEncoder(ctx).Encode(map[string]string{"error": "Невалидный токен"})
			utils.LogResponse("RequireAuth", fasthttp.StatusUnauthorized, time.Since(startTime))
			return
		}

		// Кладём user_id в контекст запроса для хэндлеров
		ctx.SetUserValue("user_id", claims.UserID)
		utils.LogDebug("Middleware", "Аутентифицирован пользователь: %s", claims.UserID)

		next(ctx)
	}
}
