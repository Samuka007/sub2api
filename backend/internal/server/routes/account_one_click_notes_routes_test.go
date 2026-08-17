package routes

import (
	"bytes"
	"context"
	"errors"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/handler"
	adminhandler "github.com/Wei-Shaw/sub2api/internal/handler/admin"
	servermiddleware "github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

type oneClickAccountNotesRouteAdminService struct {
	service.AdminService

	previewCalls int
	applyCalls   int
	previewBody  []byte
	applyBody    []byte
	applyDigest  string
}

type oneClickAccountNotesRouteUserRepository struct {
	service.UserRepository
	users map[int64]*service.User
}

type oneClickAccountNotesRouteIdempotencyRepository struct {
	record *service.IdempotencyRecord
}

func (r *oneClickAccountNotesRouteIdempotencyRepository) WithinTransaction(
	ctx context.Context,
	execute func(context.Context, service.IdempotencyRepository) error,
) error {
	return execute(ctx, r)
}

func (r *oneClickAccountNotesRouteIdempotencyRepository) CreateProcessing(
	_ context.Context,
	record *service.IdempotencyRecord,
) (bool, error) {
	if r.record != nil {
		return false, nil
	}
	clone := *record
	clone.ID = 1
	record.ID = clone.ID
	r.record = &clone
	return true, nil
}

func (r *oneClickAccountNotesRouteIdempotencyRepository) GetByScopeAndKeyHash(
	_ context.Context,
	scope string,
	keyHash string,
) (*service.IdempotencyRecord, error) {
	if r.record == nil || r.record.Scope != scope || r.record.IdempotencyKeyHash != keyHash {
		return nil, nil
	}
	clone := *r.record
	return &clone, nil
}

func (r *oneClickAccountNotesRouteIdempotencyRepository) TryReclaim(
	context.Context,
	int64,
	string,
	string,
	time.Time,
	time.Time,
	time.Time,
) (bool, error) {
	return false, nil
}

func (r *oneClickAccountNotesRouteIdempotencyRepository) ExtendProcessingLock(
	_ context.Context,
	id int64,
	requestFingerprint string,
	expectedLockedUntil time.Time,
	newLockedUntil time.Time,
	newExpiresAt time.Time,
) (bool, error) {
	if r.record == nil || r.record.ID != id ||
		r.record.Status != service.IdempotencyStatusProcessing ||
		r.record.RequestFingerprint != requestFingerprint ||
		r.record.LockedUntil == nil || !r.record.LockedUntil.Equal(expectedLockedUntil) {
		return false, nil
	}
	r.record.LockedUntil = &newLockedUntil
	r.record.ExpiresAt = newExpiresAt
	return true, nil
}

func (r *oneClickAccountNotesRouteIdempotencyRepository) MarkSucceeded(
	_ context.Context,
	id int64,
	expectedLockedUntil time.Time,
	responseStatus int,
	responseBody string,
	expiresAt time.Time,
) error {
	if r.record == nil || r.record.ID != id || r.record.Status != service.IdempotencyStatusProcessing ||
		r.record.LockedUntil == nil || !r.record.LockedUntil.Equal(expectedLockedUntil) {
		return errors.New("idempotency record is not processing")
	}
	r.record.Status = service.IdempotencyStatusSucceeded
	r.record.ResponseStatus = &responseStatus
	r.record.ResponseBody = &responseBody
	r.record.LockedUntil = nil
	r.record.ExpiresAt = expiresAt
	return nil
}

func (r *oneClickAccountNotesRouteIdempotencyRepository) MarkFailedRetryable(
	_ context.Context,
	id int64,
	expectedLockedUntil time.Time,
	errorReason string,
	lockedUntil time.Time,
	expiresAt time.Time,
) error {
	if r.record == nil || r.record.ID != id || r.record.Status != service.IdempotencyStatusProcessing ||
		r.record.LockedUntil == nil || !r.record.LockedUntil.Equal(expectedLockedUntil) {
		return errors.New("idempotency record is not processing")
	}
	r.record.Status = service.IdempotencyStatusFailedRetryable
	r.record.ErrorReason = &errorReason
	r.record.LockedUntil = &lockedUntil
	r.record.ExpiresAt = expiresAt
	return nil
}

func (r *oneClickAccountNotesRouteIdempotencyRepository) DeleteExpired(
	context.Context,
	time.Time,
	int,
) (int64, error) {
	return 0, nil
}

func (r *oneClickAccountNotesRouteUserRepository) GetByID(_ context.Context, id int64) (*service.User, error) {
	user, ok := r.users[id]
	if !ok {
		return nil, service.ErrUserNotFound
	}
	clone := *user
	return &clone, nil
}

func (r *oneClickAccountNotesRouteUserRepository) GetUserAvatar(context.Context, int64) (*service.UserAvatar, error) {
	return nil, nil
}

func (s *oneClickAccountNotesRouteAdminService) PreviewOneClickAccountNotes(_ context.Context, content []byte) (*service.OneClickAccountNotesPreview, error) {
	s.previewCalls++
	s.previewBody = append([]byte(nil), content...)
	return &service.OneClickAccountNotesPreview{}, nil
}

func (s *oneClickAccountNotesRouteAdminService) ApplyOneClickAccountNotes(_ context.Context, content []byte, previewDigest string) (*service.OneClickAccountNotesApplyResult, error) {
	s.applyCalls++
	s.applyBody = append([]byte(nil), content...)
	s.applyDigest = previewDigest
	return &service.OneClickAccountNotesApplyResult{}, nil
}

func buildOneClickAccountNotesRouteRequest(t *testing.T, path, authorization, idempotencyKey, previewDigest string, content []byte) *http.Request {
	t.Helper()
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	file, err := writer.CreateFormFile("file", "notes.txt")
	require.NoError(t, err)
	_, err = file.Write(content)
	require.NoError(t, err)
	if previewDigest != "" {
		require.NoError(t, writer.WriteField("preview_digest", previewDigest))
	}
	require.NoError(t, writer.Close())

	request := httptest.NewRequest(http.MethodPost, path, &body)
	request.Header.Set("Content-Type", writer.FormDataContentType())
	if authorization != "" {
		request.Header.Set("Authorization", authorization)
	}
	if idempotencyKey != "" {
		request.Header.Set("Idempotency-Key", idempotencyKey)
	}
	return request
}

func setupOneClickAccountNotesRouteTest(t *testing.T) (*gin.Engine, *oneClickAccountNotesRouteAdminService, string, string) {
	t.Helper()

	admin := &service.User{
		ID:                   1,
		Email:                "admin-route-test@example.com",
		Role:                 service.RoleAdmin,
		Roles:                []string{service.RoleSuperAdmin},
		Status:               service.StatusActive,
		TokenVersion:         1,
		TokenVersionResolved: true,
		Concurrency:          1,
	}
	user := &service.User{
		ID:                   2,
		Email:                "user-route-test@example.com",
		Role:                 service.RoleUser,
		Status:               service.StatusActive,
		TokenVersion:         1,
		TokenVersionResolved: true,
		Concurrency:          1,
	}
	userRepository := &oneClickAccountNotesRouteUserRepository{users: map[int64]*service.User{
		admin.ID: admin,
		user.ID:  user,
	}}
	cfg := &config.Config{JWT: config.JWTConfig{
		Secret:     "one-click-account-notes-route-test-secret",
		ExpireHour: 1,
	}}
	authService := service.NewAuthService(
		nil, userRepository, nil, nil, cfg, nil, nil, nil, nil, nil, nil, nil, nil,
	)
	userService := service.NewUserService(userRepository, nil, nil, nil)
	adminToken, err := authService.GenerateToken(context.Background(), admin)
	require.NoError(t, err)
	userToken, err := authService.GenerateToken(context.Background(), user)
	require.NoError(t, err)

	router := gin.New()
	adminService := &oneClickAccountNotesRouteAdminService{}
	accountHandler := adminhandler.NewAccountHandler(
		adminService,
		nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil,
	)
	adminAuth := servermiddleware.NewAdminAuthMiddleware(authService, userService, nil, nil)
	auditLog := servermiddleware.AuditLogMiddleware(func(c *gin.Context) { c.Next() })
	stepUp := servermiddleware.StepUpAuthMiddleware(func(c *gin.Context) { c.Next() })

	RegisterAdminRoutes(
		router.Group("/api/v1"),
		&handler.Handlers{Admin: &handler.AdminHandlers{Account: accountHandler}},
		adminAuth,
		auditLog,
		stepUp,
		nil,
		nil,
	)

	return router, adminService, "Bearer " + adminToken, "Bearer " + userToken
}

func TestAdminRoutesProtectAndDispatchOneClickAccountNotes(t *testing.T) {
	gin.SetMode(gin.TestMode)
	service.SetDefaultIdempotencyCoordinator(nil)
	t.Cleanup(func() { service.SetDefaultIdempotencyCoordinator(nil) })

	content := []byte("user@example.com---https://mail.example.test/inbox?token=route-auth-canary")
	for _, endpoint := range []struct {
		name           string
		path           string
		previewDigest  string
		idempotencyKey string
	}{
		{name: "preview", path: "/api/v1/admin/accounts/one-click-notes/preview"},
		{name: "apply", path: "/api/v1/admin/accounts/one-click-notes/apply", previewDigest: "preview-digest", idempotencyKey: "apply-key"},
	} {
		endpoint := endpoint
		t.Run(endpoint.name, func(t *testing.T) {
			router, adminService, adminAuthorization, userAuthorization := setupOneClickAccountNotesRouteTest(t)
			if endpoint.name == "apply" {
				service.SetDefaultIdempotencyCoordinator(service.NewIdempotencyCoordinator(
					&oneClickAccountNotesRouteIdempotencyRepository{},
					service.DefaultIdempotencyConfig(),
				))
			}

			for _, denied := range []struct {
				name          string
				authorization string
				wantStatus    int
			}{
				{name: "without_credentials", wantStatus: http.StatusUnauthorized},
				{name: "role_user", authorization: userAuthorization, wantStatus: http.StatusForbidden},
			} {
				denied := denied
				t.Run(denied.name, func(t *testing.T) {
					recorder := httptest.NewRecorder()
					router.ServeHTTP(recorder, buildOneClickAccountNotesRouteRequest(
						t,
						endpoint.path,
						denied.authorization,
						endpoint.idempotencyKey,
						endpoint.previewDigest,
						content,
					))

					require.Equal(t, denied.wantStatus, recorder.Code)
					require.Zero(t, adminService.previewCalls)
					require.Zero(t, adminService.applyCalls)
					require.NotContains(t, recorder.Body.String(), string(content))
					require.NotContains(t, recorder.Body.String(), "user@example.com")
					require.NotContains(t, recorder.Body.String(), "route-auth-canary")
				})
			}

			recorder := httptest.NewRecorder()
			router.ServeHTTP(recorder, buildOneClickAccountNotesRouteRequest(
				t,
				endpoint.path,
				adminAuthorization,
				endpoint.idempotencyKey,
				endpoint.previewDigest,
				content,
			))

			require.Equal(t, http.StatusOK, recorder.Code)
			if endpoint.name == "preview" {
				require.Equal(t, 1, adminService.previewCalls)
				require.Zero(t, adminService.applyCalls)
				require.Equal(t, content, adminService.previewBody)
				return
			}
			require.Zero(t, adminService.previewCalls)
			require.Equal(t, 1, adminService.applyCalls)
			require.Equal(t, content, adminService.applyBody)
			require.Equal(t, endpoint.previewDigest, adminService.applyDigest)
		})
	}
}
