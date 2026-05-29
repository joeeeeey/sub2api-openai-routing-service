package handler

import (
	"strings"

	middleware2 "github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/gin-gonic/gin"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

type openAICompatReasoningDecision struct {
	Downstream string
	Effective  string
	Source     string
}

func applyOpenAICompatReasoningDefaults(c *gin.Context, body []byte, requestKind string) ([]byte, openAICompatReasoningDecision, error) {
	decision := openAICompatReasoningDecision{}
	if !middleware2.IsOpenAICompatRequest(c) || len(body) == 0 {
		return body, decision, nil
	}

	switch requestKind {
	case "chat_completions":
		decision.Downstream = normalizeCompatEffort(strings.TrimSpace(gjson.GetBytes(body, "reasoning_effort").String()))
		if decision.Downstream == "" {
			decision.Downstream = normalizeCompatEffort(strings.TrimSpace(gjson.GetBytes(body, "extra_body.reasoning_effort").String()))
			if decision.Downstream != "" {
				decision.Source = "extra_body"
			}
		} else {
			decision.Source = "request"
		}
		if decision.Downstream == "" {
			return body, decision, nil
		}
		decision.Effective = decision.Downstream
		if decision.Source == "extra_body" {
			updated, err := sjson.SetBytes(body, "reasoning_effort", decision.Downstream)
			if err != nil {
				return body, decision, err
			}
			return updated, decision, nil
		}
		return body, decision, nil

	case "responses":
		decision.Downstream = normalizeCompatEffort(strings.TrimSpace(gjson.GetBytes(body, "reasoning.effort").String()))
		if decision.Downstream == "" {
			decision.Downstream = normalizeCompatEffort(strings.TrimSpace(gjson.GetBytes(body, "extra_body.reasoning_effort").String()))
			if decision.Downstream != "" {
				decision.Source = "extra_body"
			}
		} else {
			decision.Source = "request"
		}
		if decision.Downstream == "" {
			return body, decision, nil
		}
		decision.Effective = decision.Downstream
		if decision.Source == "extra_body" {
			updated, err := sjson.SetBytes(body, "reasoning.effort", decision.Downstream)
			if err != nil {
				return body, decision, err
			}
			updated, err = sjson.SetBytes(updated, "reasoning.summary", "auto")
			if err != nil {
				return body, decision, err
			}
			return updated, decision, nil
		}
		if !gjson.GetBytes(body, "reasoning.summary").Exists() {
			updated, err := sjson.SetBytes(body, "reasoning.summary", "auto")
			if err != nil {
				return body, decision, err
			}
			return updated, decision, nil
		}
		return body, decision, nil
	default:
		return body, decision, nil
	}
}

func normalizeCompatEffort(v string) string {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "", "default":
		return ""
	case "minimal":
		return "none"
	case "none", "low", "medium", "high":
		return strings.ToLower(strings.TrimSpace(v))
	default:
		return strings.ToLower(strings.TrimSpace(v))
	}
}
