package routes

import (
	"net/http"
	"strings"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/handler"
	"github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
)

func RegisterOpenAICompatServiceRoutes(
	r *gin.Engine,
	h *handler.Handlers,
	apiKeyAuth middleware.APIKeyAuthMiddleware,
	openAICompatMiddleware gin.HandlerFunc,
	cfg *config.Config,
) {
	if cfg == nil || !cfg.OpenAICompatService.Enabled {
		return
	}

	prefix := strings.TrimSpace(cfg.OpenAICompatService.PathPrefix)
	if prefix == "" {
		prefix = "/openai-routing"
	}
	prefix = "/" + strings.Trim(strings.TrimSpace(prefix), "/")

	bodyLimit := middleware.RequestBodyLimit(cfg.Gateway.MaxBodySize)
	r.GET(prefix+"/healthz", func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{"ok": true})
	})

	compat := r.Group(prefix)
	compat.Use(bodyLimit)
	compat.Use(middleware.ClientRequestID())
	compat.Use(openAICompatMiddleware)
	if strings.EqualFold(strings.TrimSpace(cfg.OpenAICompatService.AuthMode), "api_key") {
		compat.Use(gin.HandlerFunc(apiKeyAuth))
		compat.Use(requireOpenAICompatGroup())
	}

	compat.GET("/v1/models", h.Gateway.Models)
	compat.POST("/v1/chat/completions", h.OpenAIGateway.ChatCompletions)
	compat.POST("/v1/embeddings", h.OpenAIGateway.Embeddings)
	compat.POST("/v1/images/generations", h.OpenAIGateway.Images)
	compat.POST("/v1/responses", h.OpenAIGateway.Responses)
	compat.POST("/v1/responses/*subpath", h.OpenAIGateway.Responses)
}

func requireOpenAICompatGroup() gin.HandlerFunc {
	return func(c *gin.Context) {
		apiKey, ok := middleware.GetAPIKeyFromContext(c)
		if !ok || apiKey == nil || apiKey.Group == nil {
			writeOpenAICompatRouteError(c, http.StatusForbidden, "permission_error", "API key must be assigned to an OpenAI group")
			return
		}
		if apiKey.Group.Platform != service.PlatformOpenAI {
			writeOpenAICompatRouteError(c, http.StatusForbidden, "permission_error", "API key must belong to an OpenAI group")
			return
		}
		c.Next()
	}
}

func writeOpenAICompatRouteError(c *gin.Context, status int, errType string, message string) {
	c.JSON(status, gin.H{
		"error": gin.H{
			"type":    errType,
			"message": message,
		},
	})
	c.Abort()
}
