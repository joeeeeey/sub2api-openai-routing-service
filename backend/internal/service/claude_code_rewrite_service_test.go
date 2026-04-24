package service

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

func TestRewriteClaudeCodeRequestBody_StripsBillingHeaderAndRewritesPromptEnv(t *testing.T) {
	profile := ClaudeCodeRewriteProfile{
		DeviceID:   "canonical-device-id",
		Version:    "2.1.81",
		UserAgent:  "claude-cli/2.1.81 (external, cli)",
		Platform:   "darwin",
		Shell:      "zsh",
		OSVersion:  "Darwin 24.4.0",
		WorkingDir: "/Users/jack/projects",
	}

	originalUserID := FormatMetadataUserID("original-device-id", "acct-123", "11111111-2222-4333-8444-555555555555", profile.Version)
	body := []byte(`{
	  "model":"claude-sonnet-4-5",
	  "metadata":{"user_id":` + quoteJSONStringForRewriteTest(originalUserID) + `},
	  "baseUrl":"https://gateway.local",
	  "gateway":"custom",
	  "system":[
	    {"type":"text","text":"x-anthropic-billing-header: cc_version=2.1.81.a1b; cc_entrypoint=cli;"},
	    {"type":"text","text":"Platform: linux\nShell: bash\nOS Version: Linux 6.5.0-generic\nWorking directory: /home/bob/repo"}
	  ],
	  "messages":[
	    {"role":"user","content":"<system-reminder>Working directory: /home/alice/code\nPlatform: linux\nShell: bash</system-reminder>hello"}
	  ]
	}`)

	rewritten, changed, err := RewriteClaudeCodeRequestBody(body, "/v1/messages", profile)
	require.NoError(t, err)
	require.True(t, changed)

	userID := ParseMetadataUserID(gjson.GetBytes(rewritten, "metadata.user_id").String())
	require.NotNil(t, userID)
	require.Equal(t, profile.DeviceID, userID.DeviceID)
	require.Equal(t, "acct-123", userID.AccountUUID)
	require.Equal(t, "11111111-2222-4333-8444-555555555555", userID.SessionID)

	system := gjson.GetBytes(rewritten, "system")
	require.True(t, system.Exists())
	require.Len(t, system.Array(), 1)
	systemText := system.Array()[0].Get("text").String()
	require.Contains(t, systemText, "Platform: darwin")
	require.Contains(t, systemText, "Shell: zsh")
	require.Contains(t, systemText, "OS Version: Darwin 24.4.0")
	require.Contains(t, systemText, "Working directory: /Users/jack/projects")
	require.NotContains(t, system.Raw, "x-anthropic-billing-header")

	messageText := gjson.GetBytes(rewritten, "messages.0.content").String()
	require.Contains(t, messageText, "/Users/jack/projects")
	require.NotContains(t, messageText, "/home/alice/")

	require.False(t, gjson.GetBytes(rewritten, "baseUrl").Exists())
	require.False(t, gjson.GetBytes(rewritten, "gateway").Exists())
}

func TestApplyClaudeCodeUpstreamHeaderRewrite_StripsBillingHeaderAndCanonicalizesUserAgent(t *testing.T) {
	profile := ClaudeCodeRewriteProfile{
		Version:   "2.1.81",
		UserAgent: "claude-cli/2.1.81 (external, cli)",
	}

	req := httptest.NewRequest(http.MethodPost, "https://api.anthropic.com/v1/messages", nil)
	req.Header.Set("User-Agent", "claude-cli/2.1.81 (darwin; arm64)")
	req.Header.Set("x-anthropic-billing-header", "cc_version=2.1.81.a1b")

	ApplyClaudeCodeUpstreamHeaderRewrite(req, profile)

	require.Equal(t, profile.UserAgent, req.Header.Get("User-Agent"))
	require.Empty(t, req.Header.Get("x-anthropic-billing-header"))
}

func TestGatewayService_AnthropicOAuth_ClaudeCodeForwardAppliesRewrite(t *testing.T) {
	gin.SetMode(gin.TestMode)

	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/messages", nil)
	c.Request.Header.Set("User-Agent", "claude-cli/2.1.81 (darwin; arm64)")
	c.Request = c.Request.WithContext(SetClaudeCodeClient(c.Request.Context(), true))

	body := []byte(`{"model":"claude-sonnet-4-5","metadata":{"user_id":"{\"device_id\":\"orig-device\",\"account_uuid\":\"acct-1\",\"session_id\":\"11111111-2222-4333-8444-555555555555\"}"},"system":[{"type":"text","text":"x-anthropic-billing-header: cc_version=2.1.81.a1b;"},{"type":"text","text":"Platform: linux\nShell: bash\nOS Version: Linux 6.5.0-generic\nWorking directory: /home/bob/repo"}],"messages":[{"role":"user","content":[{"type":"text","text":"hello"}]}]}`)
	parsed, err := ParseGatewayRequest(body, PlatformAnthropic)
	require.NoError(t, err)

	upstream := &anthropicHTTPUpstreamRecorder{
		resp: &http.Response{
			StatusCode: http.StatusOK,
			Header: http.Header{
				"Content-Type": []string{"application/json"},
				"x-request-id": []string{"rid-claude-rewrite"},
			},
			Body: io.NopCloser(strings.NewReader(`{"id":"msg_1","type":"message","role":"assistant","model":"claude-sonnet-4-5-20250929","content":[{"type":"text","text":"ok"}],"usage":{"input_tokens":12,"output_tokens":7}}`)),
		},
	}

	cfg := &config.Config{
		Gateway: config.GatewayConfig{
			MaxLineSize: defaultMaxLineSize,
		},
	}
	svc := &GatewayService{
		cfg:                  cfg,
		responseHeaderFilter: compileResponseHeaderFilter(cfg),
		httpUpstream:         upstream,
		rateLimitService:     &RateLimitService{},
		deferredService:      &DeferredService{},
	}

	account := &Account{
		ID:          401,
		Name:        "anthropic-oauth-claude-code",
		Platform:    PlatformAnthropic,
		Type:        AccountTypeOAuth,
		Concurrency: 1,
		Credentials: map[string]any{
			"access_token": "oauth-token",
		},
		Status:      StatusActive,
		Schedulable: true,
	}

	result, err := svc.Forward(c.Request.Context(), c, account, parsed)
	require.NoError(t, err)
	require.NotNil(t, result)
	require.NotNil(t, upstream.lastReq)
	require.Equal(t, "Bearer oauth-token", getHeaderRaw(upstream.lastReq.Header, "authorization"))
	require.Equal(t, "claude-cli/2.1.81 (external, cli)", upstream.lastReq.Header.Get("User-Agent"))
	require.Empty(t, upstream.lastReq.Header.Get("x-anthropic-billing-header"))

	system := gjson.GetBytes(upstream.lastBody, "system")
	require.True(t, system.Exists())
	require.NotContains(t, system.Raw, "x-anthropic-billing-header")
	require.Contains(t, system.Raw, "Platform: darwin")
}

func TestGatewayService_AnthropicOAuth_ClaudeCodeCountTokensAppliesRewrite(t *testing.T) {
	gin.SetMode(gin.TestMode)

	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/messages/count_tokens", nil)
	c.Request.Header.Set("User-Agent", "claude-cli/2.1.81 (darwin; arm64)")
	c.Request = c.Request.WithContext(SetClaudeCodeClient(c.Request.Context(), true))

	body := []byte(`{"model":"claude-sonnet-4-5","system":"x-anthropic-billing-header: cc_version=2.1.81.a1b;\nWorking directory: /home/bob/repo","messages":[{"role":"user","content":[{"type":"text","text":"hello"}]}]}`)
	parsed, err := ParseGatewayRequest(body, PlatformAnthropic)
	require.NoError(t, err)

	upstream := &anthropicHTTPUpstreamRecorder{
		resp: &http.Response{
			StatusCode: http.StatusOK,
			Header: http.Header{
				"Content-Type": []string{"application/json"},
				"x-request-id": []string{"rid-claude-count"},
			},
			Body: io.NopCloser(strings.NewReader(`{"input_tokens":42}`)),
		},
	}

	cfg := &config.Config{
		Gateway: config.GatewayConfig{
			MaxLineSize: defaultMaxLineSize,
		},
	}
	svc := &GatewayService{
		cfg:                  cfg,
		responseHeaderFilter: compileResponseHeaderFilter(cfg),
		httpUpstream:         upstream,
		rateLimitService:     &RateLimitService{},
	}

	account := &Account{
		ID:          402,
		Name:        "anthropic-oauth-count-tokens",
		Platform:    PlatformAnthropic,
		Type:        AccountTypeOAuth,
		Concurrency: 1,
		Credentials: map[string]any{
			"access_token": "oauth-token",
		},
		Status:      StatusActive,
		Schedulable: true,
	}

	err = svc.ForwardCountTokens(c.Request.Context(), c, account, parsed)
	require.NoError(t, err)
	require.NotNil(t, upstream.lastReq)
	require.Equal(t, "claude-cli/2.1.81 (external, cli)", upstream.lastReq.Header.Get("User-Agent"))
	require.NotContains(t, string(upstream.lastBody), "x-anthropic-billing-header")
	require.Contains(t, string(upstream.lastBody), "/Users/claude/projects")
}

func quoteJSONStringForRewriteTest(v string) string {
	return `"` + strings.ReplaceAll(strings.ReplaceAll(v, `\`, `\\`), `"`, `\"`) + `"`
}
