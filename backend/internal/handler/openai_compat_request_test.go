package handler

import (
	"net/http/httptest"
	"testing"

	middleware2 "github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/gin-gonic/gin"
	"github.com/tidwall/gjson"
)

func TestApplyOpenAICompatReasoningDefaultsChatOmitsReasoningWhenUnset(t *testing.T) {
	gin.SetMode(gin.TestMode)
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Set(string(middleware2.ContextKeyOpenAICompatRequest), true)
	c.Set(string(middleware2.ContextKeyOpenAICompatReasoningDefault), "low")

	body := []byte(`{"model":"gpt-5.4","messages":[{"role":"user","content":"hello"}]}`)
	updated, decision, err := applyOpenAICompatReasoningDefaults(c, body, "chat_completions")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if gjson.GetBytes(updated, "reasoning_effort").Exists() {
		t.Fatalf("expected reasoning_effort to be omitted, got %s", string(updated))
	}
	if decision.Downstream != "" || decision.Effective != "" || decision.Source != "" {
		t.Fatalf("unexpected decision: %#v", decision)
	}
}

func TestApplyOpenAICompatReasoningDefaultsChatUsesExtraBodyValue(t *testing.T) {
	gin.SetMode(gin.TestMode)
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Set(string(middleware2.ContextKeyOpenAICompatRequest), true)
	c.Set(string(middleware2.ContextKeyOpenAICompatReasoningDefault), "low")

	body := []byte(`{"model":"gpt-5.4","messages":[{"role":"user","content":"hello"}],"extra_body":{"reasoning_effort":"high"}}`)
	updated, decision, err := applyOpenAICompatReasoningDefaults(c, body, "chat_completions")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got := gjson.GetBytes(updated, "reasoning_effort").String(); got != "high" {
		t.Fatalf("expected reasoning_effort=high from extra_body, got %q", got)
	}
	if decision.Effective != "high" || decision.Source != "extra_body" {
		t.Fatalf("unexpected decision: %#v", decision)
	}
}

func TestApplyOpenAICompatReasoningDefaultsResponsesOmitsReasoningWhenUnset(t *testing.T) {
	gin.SetMode(gin.TestMode)
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Set(string(middleware2.ContextKeyOpenAICompatRequest), true)
	c.Set(string(middleware2.ContextKeyOpenAICompatReasoningDefault), "low")

	body := []byte(`{"model":"gpt-5.4","input":"hello"}`)
	updated, decision, err := applyOpenAICompatReasoningDefaults(c, body, "responses")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if gjson.GetBytes(updated, "reasoning.effort").Exists() {
		t.Fatalf("expected reasoning.effort to be omitted, got %s", string(updated))
	}
	if gjson.GetBytes(updated, "reasoning.summary").Exists() {
		t.Fatalf("expected reasoning.summary to be omitted, got %s", string(updated))
	}
	if decision.Downstream != "" || decision.Effective != "" || decision.Source != "" {
		t.Fatalf("unexpected decision: %#v", decision)
	}
}
