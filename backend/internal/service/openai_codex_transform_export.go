package service

// OpenAICodexTransformResult exposes the reusable subset of the internal
// Codex transform output for local tools and development servers.
type OpenAICodexTransformResult struct {
	Modified        bool
	NormalizedModel string
	PromptCacheKey  string
}

// ApplyOpenAICodexOAuthTransform applies the same request normalization used by
// the OpenAI OAuth gateway before forwarding to ChatGPT Codex upstream.
func ApplyOpenAICodexOAuthTransform(reqBody map[string]any, isCodexCLI bool, isCompact bool) OpenAICodexTransformResult {
	result := applyCodexOAuthTransform(reqBody, isCodexCLI, isCompact)
	return OpenAICodexTransformResult{
		Modified:        result.Modified,
		NormalizedModel: result.NormalizedModel,
		PromptCacheKey:  result.PromptCacheKey,
	}
}
