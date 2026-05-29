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
	"github.com/Wei-Shaw/sub2api/internal/pkg/tlsfingerprint"
	servermiddleware "github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

type openAICompatTestAccountRepo struct {
	service.AccountRepository
	accounts []service.Account
}

func (r openAICompatTestAccountRepo) GetByID(ctx context.Context, id int64) (*service.Account, error) {
	for i := range r.accounts {
		if r.accounts[i].ID == id {
			account := r.accounts[i]
			return &account, nil
		}
	}
	return nil, errors.New("account not found")
}

func (r openAICompatTestAccountRepo) ListSchedulableByGroupIDAndPlatform(ctx context.Context, groupID int64, platform string) ([]service.Account, error) {
	var result []service.Account
	for _, acc := range r.accounts {
		if acc.Platform == platform && acc.Schedulable && acc.Status == service.StatusActive {
			result = append(result, acc)
		}
	}
	return result, nil
}

func (r openAICompatTestAccountRepo) ListSchedulableByPlatform(ctx context.Context, platform string) ([]service.Account, error) {
	return r.ListSchedulableByGroupIDAndPlatform(ctx, 0, platform)
}

func (r openAICompatTestAccountRepo) ListSchedulableUngroupedByPlatform(ctx context.Context, platform string) ([]service.Account, error) {
	return r.ListSchedulableByGroupIDAndPlatform(ctx, 0, platform)
}

type openAICompatTestUsageLogRepo struct {
	service.UsageLogRepository
	last *service.UsageLog
}

func (r *openAICompatTestUsageLogRepo) Create(ctx context.Context, log *service.UsageLog) (bool, error) {
	r.last = log
	return true, nil
}

type openAICompatTestHTTPUpstream struct {
	lastReq  *http.Request
	lastBody []byte
	resp     *http.Response
	err      error
}

func (u *openAICompatTestHTTPUpstream) Do(req *http.Request, proxyURL string, accountID int64, accountConcurrency int) (*http.Response, error) {
	u.lastReq = req
	if req != nil && req.Body != nil {
		body, _ := io.ReadAll(req.Body)
		u.lastBody = body
		_ = req.Body.Close()
		req.Body = io.NopCloser(bytes.NewReader(body))
	}
	if u.err != nil {
		return nil, u.err
	}
	return u.resp, nil
}

func (u *openAICompatTestHTTPUpstream) DoWithTLS(req *http.Request, proxyURL string, accountID int64, accountConcurrency int, tlsProfile *tlsfingerprint.Profile) (*http.Response, error) {
	return u.Do(req, proxyURL, accountID, accountConcurrency)
}

type openAICompatTestConcurrencyCache struct {
	service.ConcurrencyCache
}

func (c openAICompatTestConcurrencyCache) AcquireAccountSlot(ctx context.Context, accountID int64, maxConcurrency int, requestID string) (bool, error) {
	return true, nil
}
func (c openAICompatTestConcurrencyCache) ReleaseAccountSlot(ctx context.Context, accountID int64, requestID string) error {
	return nil
}
func (c openAICompatTestConcurrencyCache) GetAccountConcurrency(ctx context.Context, accountID int64) (int, error) {
	return 0, nil
}
func (c openAICompatTestConcurrencyCache) GetAccountConcurrencyBatch(ctx context.Context, accountIDs []int64) (map[int64]int, error) {
	return map[int64]int{}, nil
}
func (c openAICompatTestConcurrencyCache) IncrementAccountWaitCount(ctx context.Context, accountID int64, maxWait int) (bool, error) {
	return true, nil
}
func (c openAICompatTestConcurrencyCache) DecrementAccountWaitCount(ctx context.Context, accountID int64) error {
	return nil
}
func (c openAICompatTestConcurrencyCache) GetAccountWaitingCount(ctx context.Context, accountID int64) (int, error) {
	return 0, nil
}
func (c openAICompatTestConcurrencyCache) AcquireUserSlot(ctx context.Context, userID int64, maxConcurrency int, requestID string) (bool, error) {
	return true, nil
}
func (c openAICompatTestConcurrencyCache) ReleaseUserSlot(ctx context.Context, userID int64, requestID string) error {
	return nil
}
func (c openAICompatTestConcurrencyCache) GetUserConcurrency(ctx context.Context, userID int64) (int, error) {
	return 0, nil
}
func (c openAICompatTestConcurrencyCache) IncrementWaitCount(ctx context.Context, userID int64, maxWait int) (bool, error) {
	return true, nil
}
func (c openAICompatTestConcurrencyCache) DecrementWaitCount(ctx context.Context, userID int64) error {
	return nil
}
func (c openAICompatTestConcurrencyCache) GetAccountsLoadBatch(ctx context.Context, accounts []service.AccountWithConcurrency) (map[int64]*service.AccountLoadInfo, error) {
	return map[int64]*service.AccountLoadInfo{}, nil
}
func (c openAICompatTestConcurrencyCache) GetUsersLoadBatch(ctx context.Context, users []service.UserWithConcurrency) (map[int64]*service.UserLoadInfo, error) {
	return map[int64]*service.UserLoadInfo{}, nil
}
func (c openAICompatTestConcurrencyCache) CleanupExpiredAccountSlots(ctx context.Context, accountID int64) error {
	return nil
}
func (c openAICompatTestConcurrencyCache) CleanupStaleProcessSlots(ctx context.Context, activeRequestPrefix string) error {
	return nil
}

func openAICompatCompletedEvent(id, model, text string) string {
	payload := map[string]any{
		"type": "response.completed",
		"response": map[string]any{
			"id":     id,
			"model":  model,
			"status": "completed",
			"output": []map[string]any{
				{
					"type":    "message",
					"role":    "assistant",
					"content": []map[string]any{{"type": "output_text", "text": text}},
				},
			},
			"usage": map[string]any{
				"input_tokens":  2,
				"output_tokens": 3,
			},
		},
	}
	body, _ := json.Marshal(payload)
	return string(body)
}

func openAICompatSSE(payloads ...string) *http.Response {
	var buf bytes.Buffer
	for _, payload := range payloads {
		_, _ = buf.WriteString("data: " + payload + "\n\n")
	}
	_, _ = buf.WriteString("data: [DONE]\n\n")
	return &http.Response{
		StatusCode: http.StatusOK,
		Header: http.Header{
			"Content-Type": []string{"text/event-stream"},
			"x-request-id": []string{"req_test_upstream"},
		},
		Body: io.NopCloser(bytes.NewReader(buf.Bytes())),
	}
}

func newOpenAICompatTestRouter(t *testing.T, groupPlatform string, upstream *openAICompatTestHTTPUpstream) *gin.Engine {
	t.Helper()
	gin.SetMode(gin.TestMode)

	cfg := &config.Config{}
	cfg.RunMode = config.RunModeSimple
	cfg.Security.URLAllowlist.Enabled = false
	cfg.Gateway.MaxBodySize = 1 << 20
	cfg.OpenAICompatService.Enabled = true
	cfg.OpenAICompatService.AuthMode = "api_key"
	cfg.OpenAICompatService.PathPrefix = "/openai-routing"
	cfg.OpenAICompatService.DefaultReasoningEffort = "low"

	user := &service.User{
		ID:          9001,
		Email:       "tester@example.com",
		Role:        service.RoleUser,
		Status:      service.StatusActive,
		Balance:     10,
		Concurrency: 3,
	}
	groupID := int64(7001)
	apiKey := &service.APIKey{
		ID:      8001,
		Key:     "sk-test",
		Status:  service.StatusActive,
		User:    user,
		UserID:  user.ID,
		GroupID: &groupID,
		Group: &service.Group{
			ID:             groupID,
			Name:           "openai-group",
			Platform:       groupPlatform,
			Status:         service.StatusActive,
			RateMultiplier: 1,
		},
	}

	account := service.Account{
		ID:          6001,
		Name:        "OpenAI OAuth Test",
		Platform:    service.PlatformOpenAI,
		Type:        service.AccountTypeOAuth,
		Status:      service.StatusActive,
		Schedulable: true,
		Concurrency: 3,
		Credentials: map[string]any{
			"access_token":       "oauth-token",
			"chatgpt_account_id": "chatgpt-account-id",
			"refresh_token":      "refresh-token",
		},
	}

	accountRepo := openAICompatTestAccountRepo{accounts: []service.Account{account}}
	usageRepo := &openAICompatTestUsageLogRepo{}
	concurrencySvc := service.NewConcurrencyService(openAICompatTestConcurrencyCache{})
	billingCacheSvc := service.NewBillingCacheService(nil, nil, nil, nil, nil, nil, cfg, nil)
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

func TestOpenAICompatChatCompletionsRoute_OmitsReasoningByDefaultAndExtractsInstructions(t *testing.T) {
	upstream := &openAICompatTestHTTPUpstream{
		resp: openAICompatSSE(openAICompatCompletedEvent("resp_chat", "gpt-5.4", "Hello there")),
	}
	router := newOpenAICompatTestRouter(t, service.PlatformOpenAI, upstream)

	body := `{
		"model":"gpt-5.4",
		"messages":[
			{"role":"system","content":"act as assistant"},
			{"role":"user","content":"say hello"}
		],
		"temperature":0.7,
		"top_p":0.9,
		"stream":false
	}`
	req := httptest.NewRequest(http.MethodPost, "/openai-routing/v1/chat/completions", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	router.ServeHTTP(rec, req)
	require.Equal(t, http.StatusOK, rec.Code)
	require.NotNil(t, upstream.lastReq)

	var resp map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
	require.Equal(t, "chat.completion", resp["object"])

	require.Contains(t, upstream.lastReq.URL.Path, "/backend-api/codex/responses")
	require.Equal(t, "text/event-stream", upstream.lastReq.Header.Get("accept"))
	require.Equal(t, "chatgpt.com", upstream.lastReq.Host)

	upstreamJSON := gjson.ParseBytes(upstream.lastBody)
	require.Equal(t, "act as assistant", upstreamJSON.Get("instructions").String())
	require.False(t, upstreamJSON.Get("reasoning.effort").Exists())
	require.False(t, upstreamJSON.Get("reasoning.summary").Exists())
	require.True(t, upstreamJSON.Get("store").Exists())
	require.False(t, upstreamJSON.Get("store").Bool())
	require.True(t, upstreamJSON.Get("stream").Bool())
	require.False(t, upstreamJSON.Get("temperature").Exists())
	require.False(t, upstreamJSON.Get("top_p").Exists())
	require.Equal(t, 1, len(upstreamJSON.Get("input").Array()))
	require.Equal(t, "user", upstreamJSON.Get("input.0.role").String())
}

func TestOpenAICompatChatCompletionsRoute_PreservesExplicitReasoningEffort(t *testing.T) {
	upstream := &openAICompatTestHTTPUpstream{
		resp: openAICompatSSE(openAICompatCompletedEvent("resp_reasoning", "gpt-5.4", "Reasoning ok")),
	}
	router := newOpenAICompatTestRouter(t, service.PlatformOpenAI, upstream)

	body := `{
		"model":"gpt-5.4",
		"messages":[{"role":"user","content":"say hello"}],
		"reasoning_effort":"high",
		"stream":false
	}`
	req := httptest.NewRequest(http.MethodPost, "/openai-routing/v1/chat/completions", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	router.ServeHTTP(rec, req)
	require.Equal(t, http.StatusOK, rec.Code)
	require.Equal(t, "high", gjson.GetBytes(upstream.lastBody, "reasoning.effort").String())
}

func TestOpenAICompatChatCompletionsRoute_RejectsNonOpenAIGroup(t *testing.T) {
	upstream := &openAICompatTestHTTPUpstream{
		resp: openAICompatSSE(openAICompatCompletedEvent("resp_forbidden", "gpt-5.4", "Should not happen")),
	}
	router := newOpenAICompatTestRouter(t, service.PlatformAnthropic, upstream)

	req := httptest.NewRequest(http.MethodPost, "/openai-routing/v1/chat/completions", strings.NewReader(`{"model":"gpt-5.4","messages":[{"role":"user","content":"hello"}]}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	router.ServeHTTP(rec, req)
	require.Equal(t, http.StatusForbidden, rec.Code)
	require.Nil(t, upstream.lastReq)
	require.Contains(t, rec.Body.String(), "OpenAI group")
}

func TestOpenAICompatChatCompletionsRoute_StreamReturnsChatChunks(t *testing.T) {
	upstream := &openAICompatTestHTTPUpstream{
		resp: openAICompatSSE(
			`{"type":"response.created","response":{"id":"resp_stream","model":"gpt-5.4"}}`,
			`{"type":"response.output_text.delta","delta":"Hel"}`,
			`{"type":"response.output_text.delta","delta":"lo"}`,
			`{"type":"response.completed","response":{"id":"resp_stream","model":"gpt-5.4","status":"completed","output":[{"type":"message","role":"assistant","content":[{"type":"output_text","text":"Hello"}]}],"usage":{"input_tokens":2,"output_tokens":3}}}`,
		),
	}
	router := newOpenAICompatTestRouter(t, service.PlatformOpenAI, upstream)

	req := httptest.NewRequest(http.MethodPost, "/openai-routing/v1/chat/completions", strings.NewReader(`{"model":"gpt-5.4","messages":[{"role":"user","content":"hello"}],"stream":true,"stream_options":{"include_usage":true}}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	router.ServeHTTP(rec, req)
	require.Equal(t, http.StatusOK, rec.Code)
	body := rec.Body.String()
	require.Contains(t, body, `"chat.completion.chunk"`)
	require.Contains(t, body, `"content":"Hel"`)
	require.Contains(t, body, `"prompt_tokens":2`)
	require.Contains(t, body, `[DONE]`)
}

func TestOpenAICompatResponsesRoute_OmitsReasoningByDefaultAndConvertsStringInputForOAuth(t *testing.T) {
	upstream := &openAICompatTestHTTPUpstream{
		resp: openAICompatSSE(openAICompatCompletedEvent("resp_direct", "gpt-5.4", "Direct response")),
	}
	router := newOpenAICompatTestRouter(t, service.PlatformOpenAI, upstream)

	req := httptest.NewRequest(http.MethodPost, "/openai-routing/v1/responses", strings.NewReader(`{"model":"gpt-5.4","input":"hello"}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	router.ServeHTTP(rec, req)
	require.Equal(t, http.StatusOK, rec.Code)
	require.False(t, gjson.GetBytes(upstream.lastBody, "reasoning.effort").Exists())
	require.False(t, gjson.GetBytes(upstream.lastBody, "reasoning.summary").Exists())
	require.Equal(t, "user", gjson.GetBytes(upstream.lastBody, "input.0.role").String())
	require.Equal(t, "hello", gjson.GetBytes(upstream.lastBody, "input.0.content").String())
}
