package grpc

import (
	"context"
	"errors"
	"log/slog"
	"time"
	"unicode/utf8"

	"bank_proto_microservice/internal/apperr"
	"bank_proto_microservice/internal/auth/models"
	"bank_proto_microservice/internal/auth/repository"
	"bank_proto_microservice/internal/auth/service"
	authpb "bank_proto_microservice/proto/auth"

	"github.com/google/uuid"
	"google.golang.org/grpc/codes"
)

const (
	minPasswordLen = 6
	maxPasswordLen = 72 // bcrypt учитывает только первые 72 байта
	maxNameLen     = 64
)

type AuthServer struct {
	authpb.UnimplementedAuthServiceServer
	authService *service.AuthService
	userRepo    *repository.UserRepository
}

func NewAuthServer(authService *service.AuthService, userRepo *repository.UserRepository) *AuthServer {
	return &AuthServer{authService: authService, userRepo: userRepo}
}

func invalid(msg string) error {
	return apperr.New(codes.InvalidArgument, apperr.ReasonInvalidArgument, msg)
}

func internal(msg string, err error) error {
	if e := apperr.FromContext(err); e != nil {
		return e
	}
	slog.Error(msg, "err", err)
	return apperr.New(codes.Internal, apperr.ReasonInternal, msg)
}

func (s *AuthServer) Register(ctx context.Context, req *authpb.RegisterRequest) (*authpb.RegisterResponse, error) {
	name, password := req.GetName(), req.GetPassword()
	switch {
	case name == "" || password == "":
		return nil, invalid("имя и пароль обязательны")
	case utf8.RuneCountInString(name) > maxNameLen:
		return nil, invalid("имя длиннее 64 символов")
	case len(password) < minPasswordLen:
		return nil, invalid("пароль должен быть не менее 6 символов")
	case len(password) > maxPasswordLen:
		return nil, invalid("пароль должен быть не длиннее 72 байт")
	}

	hash, err := s.authService.HashPassword(password)
	if err != nil {
		return nil, internal("ошибка хеширования пароля", err)
	}
	user := &models.User{Name: name, PasswordHash: hash}
	if err := s.userRepo.Create(ctx, user); err != nil {
		if errors.Is(err, repository.ErrUserExists) {
			return nil, apperr.New(codes.AlreadyExists, apperr.ReasonUserExists, "пользователь с таким именем уже существует")
		}
		return nil, internal("ошибка создания пользователя", err)
	}

	return &authpb.RegisterResponse{
		UserId:    user.ID,
		Name:      user.Name,
		CreatedAt: user.CreatedAt.Format(time.RFC3339),
	}, nil
}

func (s *AuthServer) Login(ctx context.Context, req *authpb.LoginRequest) (*authpb.LoginResponse, error) {
	name, password := req.GetName(), req.GetPassword()
	if name == "" || password == "" {
		return nil, invalid("имя и пароль обязательны")
	}
	badCredentials := apperr.New(codes.Unauthenticated, apperr.ReasonBadCredentials, "неверное имя пользователя или пароль")

	user, err := s.userRepo.GetByName(ctx, name)
	if errors.Is(err, repository.ErrUserNotFound) {
		s.authService.SimulatePasswordCheck(password)
		return nil, badCredentials
	}
	if err != nil {
		return nil, internal("ошибка поиска пользователя", err)
	}
	if !s.authService.CheckPassword(password, user.PasswordHash) {
		return nil, badCredentials
	}

	token, err := s.authService.GenerateToken(user.ID)
	if err != nil {
		return nil, internal("ошибка генерации токена", err)
	}
	return &authpb.LoginResponse{
		Token:     token,
		UserId:    user.ID,
		Name:      user.Name,
		ExpiresIn: s.authService.ExpiresIn().String(),
	}, nil
}

func (s *AuthServer) DeleteUser(ctx context.Context, req *authpb.DeleteUserRequest) (*authpb.DeleteUserResponse, error) {
	if uuid.Validate(req.GetUserId()) != nil {
		return nil, invalid("user_id должен быть UUID")
	}
	if err := s.userRepo.Delete(ctx, req.GetUserId()); err != nil {
		if errors.Is(err, repository.ErrUserNotFound) {
			return nil, apperr.New(codes.NotFound, apperr.ReasonUserNotFound, "пользователь не найден")
		}
		return nil, internal("ошибка удаления пользователя", err)
	}
	return &authpb.DeleteUserResponse{Success: true, Message: "Пользователь успешно удалён"}, nil
}
