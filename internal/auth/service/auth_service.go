package service

import (
	"time"

	"github.com/golang-jwt/jwt/v4"
	"golang.org/x/crypto/bcrypt"
)

type AuthService struct {
	jwtSecret     []byte
	jwtExpiration time.Duration
	bcryptCost    int
	// Хеш-заглушка для входа несуществующего пользователя: bcrypt выполняется всегда,
	// поэтому по времени ответа нельзя узнать, существует ли имя.
	dummyHash []byte
}

func NewAuthService(secret string, expiration time.Duration, bcryptCost int) *AuthService {
	dummy, _ := bcrypt.GenerateFromPassword([]byte("dummy-password"), bcryptCost)
	return &AuthService{
		jwtSecret:     []byte(secret),
		jwtExpiration: expiration,
		bcryptCost:    bcryptCost,
		dummyHash:     dummy,
	}
}

func (s *AuthService) HashPassword(password string) (string, error) {
	hash, err := bcrypt.GenerateFromPassword([]byte(password), s.bcryptCost)
	return string(hash), err
}

func (s *AuthService) CheckPassword(password, hash string) bool {
	return bcrypt.CompareHashAndPassword([]byte(hash), []byte(password)) == nil
}

// SimulatePasswordCheck тратит столько же времени, сколько настоящая проверка пароля.
func (s *AuthService) SimulatePasswordCheck(password string) {
	_ = bcrypt.CompareHashAndPassword(s.dummyHash, []byte(password))
}

func (s *AuthService) ExpiresIn() time.Duration { return s.jwtExpiration }

type Claims struct {
	UserID string `json:"user_id"`
	jwt.RegisteredClaims
}

func (s *AuthService) GenerateToken(userID string) (string, error) {
	now := time.Now()
	claims := &Claims{
		UserID: userID,
		RegisteredClaims: jwt.RegisteredClaims{
			ExpiresAt: jwt.NewNumericDate(now.Add(s.jwtExpiration)),
			IssuedAt:  jwt.NewNumericDate(now),
		},
	}
	return jwt.NewWithClaims(jwt.SigningMethodHS256, claims).SignedString(s.jwtSecret)
}
