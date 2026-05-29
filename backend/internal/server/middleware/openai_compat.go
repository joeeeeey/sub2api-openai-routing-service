package middleware

import (
	"context"
	"net/http"
	"strings"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/pkg/ctxkey"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
)

const (
	ContextKeyOpenAICompatRequest          ContextKey = "openai_compat_request"
	ContextKeyOpenAICompatPersistUsage     ContextKey = "openai_compat_persist_usage"
	ContextKeyOpenAICompatReasoningDefault ContextKey = "openai_compat_reasoning_default"
)

func NewOpenAICompatServiceMiddleware(
	apiKeyService *service.APIKeyService,
	subscriptionService *service.SubscriptionService,
	cfg *config.Config,
) gin.HandlerFunc {
	return func(c *gin.Context) {
		if cfg == nil || !cfg.OpenAICompatService.Enabled {
			AbortWithError(c, http.StatusNotFound, "not_found", "OpenAI compat service is disabled")
			return
		}

		c.Set(string(ContextKeyOpenAICompatRequest), true)
		c.Set(string(ContextKeyOpenAICompatPersistUsage), true)
		c.Set(string(ContextKeyOpenAICompatReasoningDefault), strings.ToLower(strings.TrimSpace(cfg.OpenAICompatService.DefaultReasoningEffort)))

		mode := strings.ToLower(strings.TrimSpace(cfg.OpenAICompatService.AuthMode))
		if mode == "" {
			mode = "static_key"
		}
		if mode == "api_key" {
			c.Next()
			return
		}

		if mode == "static_key" {
			incomingKey := extractCompatAPIKey(c)
			if strings.TrimSpace(incomingKey) == "" || strings.TrimSpace(incomingKey) != strings.TrimSpace(cfg.OpenAICompatService.StaticKey) {
				AbortWithError(c, http.StatusUnauthorized, "INVALID_API_KEY", "Invalid API key")
				return
			}
		}

		serviceAPIKey := strings.TrimSpace(cfg.OpenAICompatService.ServiceAPIKey)
		if serviceAPIKey == "" {
			AbortWithError(c, http.StatusInternalServerError, "OPENAI_COMPAT_CONFIG_INVALID", "service_api_key is not configured")
			return
		}
		apiKey, err := apiKeyService.GetByKey(c.Request.Context(), serviceAPIKey)
		if err != nil || apiKey == nil {
			AbortWithError(c, http.StatusInternalServerError, "OPENAI_COMPAT_CONFIG_INVALID", "configured service_api_key not found")
			return
		}
		if apiKey.Group == nil || strings.TrimSpace(apiKey.Group.Platform) != service.PlatformOpenAI {
			AbortWithError(c, http.StatusInternalServerError, "OPENAI_COMPAT_CONFIG_INVALID", "configured service_api_key must belong to an OpenAI group")
			return
		}
		if apiKey.User == nil || !apiKey.User.IsActive() || !apiKey.IsActive() {
			AbortWithError(c, http.StatusForbidden, "OPENAI_COMPAT_SERVICE_KEY_INVALID", "configured service_api_key is not active")
			return
		}

		var subscription *service.UserSubscription
		if apiKey.Group.IsSubscriptionType() && subscriptionService != nil {
			subscription, _ = subscriptionService.GetActiveSubscription(c.Request.Context(), apiKey.User.ID, apiKey.Group.ID)
		}

		bindAuthenticatedContext(c, apiKey, subscription)
		_ = apiKeyService.TouchLastUsed(c.Request.Context(), apiKey.ID)
		c.Next()
	}
}

func IsOpenAICompatRequest(c *gin.Context) bool {
	if c == nil {
		return false
	}
	value, exists := c.Get(string(ContextKeyOpenAICompatRequest))
	if !exists {
		return false
	}
	enabled, _ := value.(bool)
	return enabled
}

func GetOpenAICompatDefaultReasoningEffort(c *gin.Context) string {
	if c == nil {
		return ""
	}
	value, exists := c.Get(string(ContextKeyOpenAICompatReasoningDefault))
	if !exists {
		return ""
	}
	effort, _ := value.(string)
	return strings.TrimSpace(effort)
}

func bindAuthenticatedContext(c *gin.Context, apiKey *service.APIKey, subscription *service.UserSubscription) {
	if c == nil || apiKey == nil {
		return
	}
	if subscription != nil {
		c.Set(string(ContextKeySubscription), subscription)
	}
	c.Set(string(ContextKeyAPIKey), apiKey)
	if apiKey.User != nil {
		c.Set(string(ContextKeyUser), AuthSubject{
			UserID:      apiKey.User.ID,
			Concurrency: apiKey.User.Concurrency,
		})
		c.Set(string(ContextKeyUserRole), apiKey.User.Role)
	}
	if service.IsGroupContextValid(apiKey.Group) {
		ctx := context.WithValue(c.Request.Context(), ctxkey.Group, apiKey.Group)
		c.Request = c.Request.WithContext(ctx)
	}
}

func extractCompatAPIKey(c *gin.Context) string {
	if c == nil {
		return ""
	}
	authHeader := c.GetHeader("Authorization")
	if authHeader != "" {
		parts := strings.SplitN(authHeader, " ", 2)
		if len(parts) == 2 && strings.EqualFold(parts[0], "Bearer") {
			return strings.TrimSpace(parts[1])
		}
	}
	if key := strings.TrimSpace(c.GetHeader("x-api-key")); key != "" {
		return key
	}
	if key := strings.TrimSpace(c.GetHeader("x-goog-api-key")); key != "" {
		return key
	}
	return ""
}
