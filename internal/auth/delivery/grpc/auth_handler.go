package grpc

import (
	"context"
	"time"

	"bank_proto_microservice/internal/auth/models"
	"bank_proto_microservice/internal/auth/repository"
	"bank_proto_microservice/internal/auth/service"
	authpb "bank_proto_microservice/proto/auth"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

type AuthServer struct {
	authpb.UnimplementedAuthServiceServer
	authService *service.AuthService
	userRepo    *repository.UserRepository
}

func NewAuthServer(authService *service.AuthService, userRepo *repository.UserRepository) *AuthServer {
	return &AuthServer{
		authService: authService,
		userRepo:    userRepo,
	}
}

func (s *AuthServer) Register(ctx context.Context, req *authpb.RegisterRequest) (*authpb.RegisterResponse, error) {
	if req.GetName() == "" || req.GetPassword() == "" {
		return nil, status.Error(codes.InvalidArgument, "Имя и пароль обязательны")
	}

	if len(req.GetPassword()) < 6 {
		return nil, status.Error(codes.InvalidArgument, "Пароль должен быть не менее 6 символов")
	}

	hash, err := s.authService.HashPassword(req.GetPassword())
	if err != nil {
		return nil, status.Error(codes.Internal, "Ошибка хеширования пароля")
	}

	user := &models.User{
		Name:         req.GetName(),
		PasswordHash: hash,
	}

	if err := s.userRepo.Create(ctx, user); err != nil {
		return nil, status.Error(codes.AlreadyExists, "Пользователь с таким именем уже существует")
	}

	return &authpb.RegisterResponse{
		UserId:    user.ID,
		Name:      user.Name,
		CreatedAt: user.CreatedAt.Format(time.RFC3339),
	}, nil
}

func (s *AuthServer) Login(ctx context.Context, req *authpb.LoginRequest) (*authpb.LoginResponse, error) {
	if req.GetName() == "" || req.GetPassword() == "" {
		return nil, status.Error(codes.InvalidArgument, "Имя и пароль обязательны")
	}

	user, err := s.userRepo.GetByName(ctx, req.GetName())
	if err != nil {
		return nil, status.Error(codes.Unauthenticated, "Неверное имя пользователя или пароль")
	}

	if err := s.authService.CheckPasswordHash(req.GetPassword(), user.PasswordHash); err != nil {
		return nil, status.Error(codes.Unauthenticated, "Неверное имя пользователя или пароль")
	}

	token, err := s.authService.GenerateToken(user.ID)
	if err != nil {
		return nil, status.Error(codes.Internal, "Ошибка генерации токена")
	}

	return &authpb.LoginResponse{
		Token:     token,
		UserId:    user.ID,
		Name:      user.Name,
		ExpiresIn: "24h",
	}, nil
}

func (s *AuthServer) DeleteUser(ctx context.Context, req *authpb.DeleteUserRequest) (*authpb.DeleteUserResponse, error) {
	if req.GetUserId() == "" {
		return nil, status.Error(codes.InvalidArgument, "user_id обязателен")
	}

	if err := s.userRepo.Delete(ctx, req.GetUserId()); err != nil {
		return nil, status.Error(codes.NotFound, "Пользователь не найден")
	}

	return &authpb.DeleteUserResponse{
		Success: true,
		Message: "Пользователь успешно удалён",
	}, nil
}
