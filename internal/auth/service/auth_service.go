package service

import (
	"fmt"
	"time"

	"bank_proto_microservice/internal/utils"

	"github.com/golang-jwt/jwt/v4"
	"golang.org/x/crypto/bcrypt"
)

type AuthService struct {
	jwtSecret     string
	jwtExpiration time.Duration
}

func NewAuthService(secret string, expiration time.Duration) *AuthService {
	utils.LogSuccess("AuthService", fmt.Sprintf("Инициализирован сервис аутентификации (TTL: %v)", expiration))
	return &AuthService{
		jwtSecret:     secret,
		jwtExpiration: expiration,
	}
}

func (s *AuthService) HashPassword(password string) (string, error) {
	hashedPassword, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	if err != nil {
		utils.LogError("AuthService", "Ошибка хеширования пароля", err)
		return "", err
	}
	return string(hashedPassword), nil
}

func (s *AuthService) CheckPasswordHash(password, hash string) error {
	return bcrypt.CompareHashAndPassword([]byte(hash), []byte(password))
}

type Claims struct {
	UserID string `json:"user_id"`
	jwt.RegisteredClaims
}

func (s *AuthService) GenerateToken(userID string) (string, error) {
	claims := &Claims{
		UserID: userID,
		RegisteredClaims: jwt.RegisteredClaims{
			ExpiresAt: jwt.NewNumericDate(time.Now().Add(s.jwtExpiration)),
		},
	}
	token := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	return token.SignedString([]byte(s.jwtSecret))
}
