package service

import (
	"context"
	"net/http"

	"github.com/gin-gonic/gin"
)

func (s *GatewayService) shouldApplyClaudeCodeRewrite(ctx context.Context, account *Account) bool {
	return account != nil && account.Platform == PlatformAnthropic && IsClaudeCodeClient(ctx)
}

func (s *GatewayService) maybeApplyClaudeCodeRequestRewrite(ctx context.Context, c *gin.Context, account *Account, body []byte) []byte {
	if !s.shouldApplyClaudeCodeRewrite(ctx, account) || len(body) == 0 {
		return body
	}

	headers := http.Header{}
	path := "/v1/messages"
	if c != nil && c.Request != nil {
		headers = c.Request.Header
		path = c.Request.URL.Path
	}

	var fp *Fingerprint
	if account.IsOAuth() && s.identityService != nil {
		fp, _ = s.identityService.GetOrCreateFingerprint(ctx, account.ID, headers)
		if fp != nil {
			accountUUID := account.GetExtraString("account_uuid")
			if accountUUID != "" && fp.ClientID != "" {
				if rewritten, err := s.identityService.RewriteUserIDWithMasking(ctx, body, account, accountUUID, fp.ClientID, fp.UserAgent); err == nil && len(rewritten) > 0 {
					body = rewritten
				}
			}
		}
	}

	profile := BuildClaudeCodeRewriteProfile(account, fp, headers)
	rewritten, changed, err := RewriteClaudeCodeRequestBody(body, path, profile)
	if err != nil || !changed {
		return body
	}
	return rewritten
}

func (s *GatewayService) maybeApplyClaudeCodeHeaderRewrite(ctx context.Context, req *http.Request, account *Account, fp *Fingerprint) {
	if !s.shouldApplyClaudeCodeRewrite(ctx, account) || req == nil {
		return
	}
	profile := BuildClaudeCodeRewriteProfile(account, fp, req.Header)
	ApplyClaudeCodeUpstreamHeaderRewrite(req, profile)
}
