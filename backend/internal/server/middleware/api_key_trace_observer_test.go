package middleware

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

type recordingAPIKeyAuthObserver struct {
	resolved []int64
	accepted []int64
}

func (o *recordingAPIKeyAuthObserver) APIKeyResolved(_ *gin.Context, key *service.APIKey) {
	o.resolved = append(o.resolved, key.ID)
}

func (o *recordingAPIKeyAuthObserver) APIKeyAccepted(_ *gin.Context, key *service.APIKey) {
	o.accepted = append(o.accepted, key.ID)
}

func TestModelTraceAuthObserverIgnoresUnknownAPIKey(t *testing.T) {
	observer, status := exerciseAPIKeyAuthObserver(t, nil, service.ErrAPIKeyNotFound)

	require.Equal(t, http.StatusUnauthorized, status)
	require.Empty(t, observer.resolved)
	require.Empty(t, observer.accepted)
}

func TestModelTraceAuthObserverSeesRecognizedFailure(t *testing.T) {
	user := &service.User{ID: 7, Status: service.StatusActive}
	key := &service.APIKey{ID: 100, UserID: user.ID, Key: "known", Status: service.StatusDisabled, User: user}
	observer, status := exerciseAPIKeyAuthObserver(t, key, nil)

	require.Equal(t, http.StatusUnauthorized, status)
	require.Equal(t, []int64{100}, observer.resolved)
	require.Empty(t, observer.accepted)
}

func TestModelTraceAuthObserverMarksAcceptedAPIKey(t *testing.T) {
	user := &service.User{ID: 7, Status: service.StatusActive, Balance: 10}
	key := &service.APIKey{ID: 101, UserID: user.ID, Key: "active", Status: service.StatusActive, User: user}
	observer, status := exerciseAPIKeyAuthObserver(t, key, nil)

	require.Equal(t, http.StatusNoContent, status)
	require.Equal(t, []int64{101}, observer.resolved)
	require.Equal(t, []int64{101}, observer.accepted)
}

func exerciseAPIKeyAuthObserver(t *testing.T, key *service.APIKey, lookupErr error) (*recordingAPIKeyAuthObserver, int) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	cfg := &config.Config{RunMode: config.RunModeSimple}
	repo := fakeAPIKeyRepo{getByKey: func(context.Context, string) (*service.APIKey, error) {
		if lookupErr != nil {
			return nil, lookupErr
		}
		clone := *key
		return &clone, nil
	}}
	svc := service.NewAPIKeyService(repo, nil, nil, nil, nil, nil, cfg)
	observer := &recordingAPIKeyAuthObserver{}
	router := gin.New()
	router.Use(func(c *gin.Context) {
		SetAPIKeyAuthObserver(c, observer)
		c.Next()
	})
	router.Use(gin.HandlerFunc(NewAPIKeyAuthMiddleware(svc, nil, cfg)))
	router.POST("/candidate", func(c *gin.Context) { c.Status(http.StatusNoContent) })

	req := httptest.NewRequest(http.MethodPost, "/candidate", nil)
	req.Header.Set("x-api-key", "candidate-key")
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)
	return observer, w.Code
}
