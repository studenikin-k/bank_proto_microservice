package grpc

import (
	"context"
	"time"

	"bank_proto_microservice/internal/auth/models"
	"bank_proto_microservice/internal/auth/repository"
	"bank_proto_microservice/internal/auth/service"
	"bank_proto_microservice/internal/utils"
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
	utils.LogSuccess("AuthServer", "Инициализирован gRPC сервер AuthService")
	return &AuthServer{
		authService: authService,
		userRepo:    userRepo,
	}
}

func (s *AuthServer) Register(ctx context.Context, req *authpb.RegisterRequest) (*authpb.RegisterResponse, error) {
	name := req.GetName()
	utils.LogInfo("AuthServer", "Запрос регистрации пользователя: %s", name)

	if name == "" || req.GetPassword() == "" {
		utils.LogWarning("AuthServer", "Отсутствуют имя или пароль")
		return nil, status.Error(codes.InvalidArgument, "Имя и пароль обязательны")
	}

	if len(req.GetPassword()) < 6 {
		utils.LogWarning("AuthServer", "Пароль слишком короткий для пользователя %s", name)
		return nil, status.Error(codes.InvalidArgument, "Пароль должен быть не менее 6 символов")
	}

	hash, err := s.authService.HashPassword(req.GetPassword())
	if err != nil {
		utils.LogError("AuthServer", "Ошибка хеширования пароля", err)
		return nil, status.Error(codes.Internal, "Ошибка хеширования пароля")
	}

	user := &models.User{
		Name:         name,
		PasswordHash: hash,
	}

	if err := s.userRepo.Create(ctx, user); err != nil {
		utils.LogError("AuthServer", "Ошибка создания пользователя в БД", err)
		return nil, status.Errorf(codes.AlreadyExists, "Пользователь '%s' уже существует или ошибка БД", name)
	}

	utils.LogSuccess("AuthServer", "Пользователь %s успешно зарегистрирован (ID: %s)", user.Name, user.ID)

	return &authpb.RegisterResponse{
		UserId:    user.ID,
		Name:      user.Name,
		CreatedAt: user.CreatedAt.Format(time.RFC3339),
	}, nil
}

func (s *AuthServer) Login(ctx context.Context, req *authpb.LoginRequest) (*authpb.LoginResponse, error) {
	name := req.GetName()
	utils.LogInfo("AuthServer", "Попытка входа пользователя: %s", name)

	if name == "" || req.GetPassword() == "" {
		utils.LogWarning("AuthServer", "Отсутствуют имя или пароль при логине")
		return nil, status.Error(codes.InvalidArgument, "Имя и пароль обязательны")
	}

	user, err := s.userRepo.GetByName(ctx, name)
	if err != nil {
		utils.LogWarning("AuthServer", "Пользователь %s не найден в базе данных", name)
		return nil, status.Error(codes.Unauthenticated, "Неверное имя пользователя или пароль")
	}

	if err := s.authService.CheckPasswordHash(req.GetPassword(), user.PasswordHash); err != nil {
		utils.LogWarning("AuthServer", "Неверный пароль для пользователя %s", name)
		return nil, status.Error(codes.Unauthenticated, "Неверное имя пользователя или пароль")
	}

	token, err := s.authService.GenerateToken(user.ID)
	if err != nil {
		utils.LogError("AuthServer", "Ошибка генерации токена", err)
		return nil, status.Error(codes.Internal, "Ошибка генерации токена")
	}

	utils.LogSuccess("AuthServer", "Вход выполнен успешно: %s (ID: %s)", user.Name, user.ID)

	return &authpb.LoginResponse{
		Token:     token,
		UserId:    user.ID,
		Name:      user.Name,
		ExpiresIn: "24h",
	}, nil
}

func (s *AuthServer) DeleteUser(ctx context.Context, req *authpb.DeleteUserRequest) (*authpb.DeleteUserResponse, error) {
	userID := req.GetUserId()
	utils.LogInfo("AuthServer", "Запрос на удаление пользователя: %s", userID)

	if userID == "" {
		utils.LogWarning("AuthServer", "user_id не передан")
		return nil, status.Error(codes.InvalidArgument, "user_id обязателен")
	}

	if err := s.userRepo.Delete(ctx, userID); err != nil {
		utils.LogError("AuthServer", "Ошибка удаления пользователя", err)
		return nil, status.Errorf(codes.NotFound, "Пользователь не найден")
	}

	utils.LogSuccess("AuthServer", "Пользователь %s успешно удалён", userID)

	return &authpb.DeleteUserResponse{
		Success: true,
		Message: "Пользователь успешно удалён",
	}, nil
}
