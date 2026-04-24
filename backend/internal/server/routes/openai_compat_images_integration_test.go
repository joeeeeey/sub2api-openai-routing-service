package routes

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/handler"
	"github.com/Wei-Shaw/sub2api/internal/pkg/ctxkey"
	servermiddleware "github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

func newOpenAICompatImagesTestRouter(t *testing.T, upstream *openAICompatTestHTTPUpstream) *gin.Engine {
	t.Helper()
	gin.SetMode(gin.TestMode)

	cfg := &config.Config{}
	cfg.RunMode = config.RunModeSimple
	cfg.Security.URLAllowlist.Enabled = false
	cfg.Gateway.MaxBodySize = 1 << 20
	cfg.OpenAICompatService.Enabled = true
	cfg.OpenAICompatService.AuthMode = "api_key"
	cfg.OpenAICompatService.PathPrefix = "/openai-routing"

	user := &service.User{
		ID:          9002,
		Email:       "image-tester@example.com",
		Role:        service.RoleUser,
		Status:      service.StatusActive,
		Balance:     10,
		Concurrency: 3,
	}
	groupID := int64(7002)
	apiKey := &service.APIKey{
		ID:      8002,
		Key:     "sk-test-image",
		Status:  service.StatusActive,
		User:    user,
		UserID:  user.ID,
		GroupID: &groupID,
		Group: &service.Group{
			ID:             groupID,
			Name:           "openai-image-group",
			Platform:       service.PlatformOpenAI,
			Status:         service.StatusActive,
			RateMultiplier: 1,
		},
	}

	account := service.Account{
		ID:          6002,
		Name:        "OpenAI API Key Image Test",
		Platform:    service.PlatformOpenAI,
		Type:        service.AccountTypeAPIKey,
		Status:      service.StatusActive,
		Schedulable: true,
		Concurrency: 3,
		Credentials: map[string]any{
			"api_key":  "upstream-api-key",
			"base_url": "https://api.openai.com",
		},
	}

	accountRepo := openAICompatTestAccountRepo{accounts: []service.Account{account}}
	usageRepo := &openAICompatTestUsageLogRepo{}
	concurrencySvc := service.NewConcurrencyService(openAICompatTestConcurrencyCache{})
	billingCacheSvc := service.NewBillingCacheService(nil, nil, nil, nil, nil, nil, cfg)
	openAISvc := service.NewOpenAIGatewayService(
		accountRepo,
		usageRepo,
		nil,
		nil,
		nil,
		nil,
		nil,
		cfg,
		nil,
		concurrencySvc,
		service.NewBillingService(cfg, nil),
		nil,
		billingCacheSvc,
		upstream,
		&service.DeferredService{},
		nil,
		nil,
		nil,
		nil,
	)
	openAIHandler := handler.NewOpenAIGatewayHandler(
		openAISvc,
		concurrencySvc,
		billingCacheSvc,
		&service.APIKeyService{},
		nil,
		nil,
		cfg,
	)

	r := gin.New()
	compatMiddleware := servermiddleware.NewOpenAICompatServiceMiddleware(nil, nil, cfg)
	apiKeyAuth := servermiddleware.APIKeyAuthMiddleware(func(c *gin.Context) {
		c.Set(string(servermiddleware.ContextKeyAPIKey), apiKey)
		c.Set(string(servermiddleware.ContextKeyUser), servermiddleware.AuthSubject{
			UserID:      user.ID,
			Concurrency: user.Concurrency,
		})
		c.Set(string(servermiddleware.ContextKeyUserRole), user.Role)
		ctx := context.WithValue(c.Request.Context(), ctxkey.Group, apiKey.Group)
		c.Request = c.Request.WithContext(ctx)
		c.Next()
	})
	RegisterOpenAICompatServiceRoutes(r, &handler.Handlers{
		Gateway:       &handler.GatewayHandler{},
		OpenAIGateway: openAIHandler,
	}, apiKeyAuth, compatMiddleware, cfg)
	return r
}

func TestOpenAICompatImagesRoute_ForwardsToImagesGenerationsAndReturnsJSON(t *testing.T) {
	upstream := &openAICompatTestHTTPUpstream{
		resp: &http.Response{
			StatusCode: http.StatusOK,
			Header: http.Header{
				"Content-Type": []string{"application/json"},
				"x-request-id": []string{"req_img_upstream"},
			},
			Body: io.NopCloser(bytes.NewBufferString(`{"created":123,"data":[{"b64_json":"ZmFrZS1pbWFnZQ=="}],"usage":{"input_tokens":4,"output_tokens":6}}`)),
		},
	}
	router := newOpenAICompatImagesTestRouter(t, upstream)

	req := httptest.NewRequest(http.MethodPost, "/openai-routing/v1/images/generations", strings.NewReader(`{"prompt":"draw a cat"}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	router.ServeHTTP(rec, req)
	require.Equal(t, http.StatusOK, rec.Code)
	require.NotNil(t, upstream.lastReq)
	require.Equal(t, "/v1/images/generations", upstream.lastReq.URL.Path)
	require.Equal(t, "Bearer upstream-api-key", upstream.lastReq.Header.Get("Authorization"))
	require.Equal(t, "gpt-image-2", gjson.GetBytes(upstream.lastBody, "model").String())
	require.Equal(t, "draw a cat", gjson.GetBytes(upstream.lastBody, "prompt").String())

	var resp map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
	require.EqualValues(t, 123, resp["created"])
	data, ok := resp["data"].([]any)
	require.True(t, ok)
	require.Len(t, data, 1)
}

func TestOpenAICompatImagesRoute_RejectsNonOpenAIGroup(t *testing.T) {
	upstream := &openAICompatTestHTTPUpstream{
		err: errors.New("should not be called"),
	}
	router := newOpenAICompatTestRouter(t, service.PlatformAnthropic, upstream)

	req := httptest.NewRequest(http.MethodPost, "/openai-routing/v1/images/generations", strings.NewReader(`{"prompt":"draw a cat"}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	router.ServeHTTP(rec, req)
	require.Equal(t, http.StatusForbidden, rec.Code)
	require.Nil(t, upstream.lastReq)
	require.Contains(t, rec.Body.String(), "OpenAI group")
}
