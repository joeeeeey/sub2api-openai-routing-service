package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	apicompat "github.com/Wei-Shaw/sub2api/internal/pkg/apicompat"
)

type recordedUpstreamRequest struct {
	Header http.Header
	Body   []byte
}

type upstreamRecorder struct {
	mu            sync.Mutex
	requests      []recordedUpstreamRequest
	tokenRequests int
}

func (r *upstreamRecorder) add(req *http.Request, body []byte) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.requests = append(r.requests, recordedUpstreamRequest{
		Header: req.Header.Clone(),
		Body:   append([]byte(nil), body...),
	})
}

func (r *upstreamRecorder) incToken() {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.tokenRequests++
}

func (r *upstreamRecorder) lastRequest(t *testing.T) recordedUpstreamRequest {
	t.Helper()
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.requests) == 0 {
		t.Fatal("expected at least one upstream request")
	}
	return r.requests[len(r.requests)-1]
}

func (r *upstreamRecorder) tokenCount() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.tokenRequests
}

func TestLocalServerHealthzAndModels(t *testing.T) {
	server, _ := newTestLocalServer(t, validTestState())
	defer server.Close()

	resp, err := http.Get(server.URL + "/healthz")
	if err != nil {
		t.Fatalf("healthz request failed: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("unexpected healthz status: %d", resp.StatusCode)
	}

	modelsResp, err := http.Get(server.URL + "/v1/models")
	if err != nil {
		t.Fatalf("models request failed: %v", err)
	}
	defer modelsResp.Body.Close()
	if modelsResp.StatusCode != http.StatusOK {
		t.Fatalf("unexpected models status: %d", modelsResp.StatusCode)
	}

	var payload modelsListResponse
	if err := json.NewDecoder(modelsResp.Body).Decode(&payload); err != nil {
		t.Fatalf("decode models response: %v", err)
	}
	if payload.Object != "list" {
		t.Fatalf("unexpected models object: %s", payload.Object)
	}
	found := false
	for _, model := range payload.Data {
		if model.ID == "gpt-5.4" {
			found = true
			break
		}
	}
	if !found {
		t.Fatal("expected gpt-5.4 in models list")
	}
}

func TestChatCompletionsBufferedTransformsSystemToInstructionsAndIgnoresAuthorization(t *testing.T) {
	recorder := &upstreamRecorder{}
	upstream := newUpstreamServer(t, recorder, func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		recorder.add(r, body)
		writeResponsesSSE(w, responsesCompletedEvent("resp_buffered", "gpt-5.4", "Hello there"))
	})
	defer upstream.Close()
	withTestUpstreamEndpoints(t, upstream.URL)

	server, statePath := newTestLocalServer(t, validTestState())
	defer server.Close()

	reqBody := map[string]any{
		"model": "gpt-5.4",
		"messages": []map[string]any{
			{"role": "system", "content": "act as assistant"},
			{"role": "user", "content": "how to fishing"},
		},
		"stream": false,
		"metadata": map[string]any{
			"trace_id":   "trace-123",
			"session_id": "session-456",
		},
		"extra_body": map[string]any{
			"user": "user-789",
		},
	}

	respBody := postJSON(t, server.URL+"/v1/chat/completions", reqBody, map[string]string{
		"Authorization": "Bearer dummy-local",
	})

	var resp apicompat.ChatCompletionsResponse
	if err := json.Unmarshal(respBody, &resp); err != nil {
		t.Fatalf("decode chat response: %v", err)
	}
	if len(resp.Choices) != 1 {
		t.Fatalf("expected 1 choice, got %d", len(resp.Choices))
	}
	content := stringContent(t, resp.Choices[0].Message.Content)
	if content != "Hello there" {
		t.Fatalf("unexpected content: %q", content)
	}

	recorded := recorder.lastRequest(t)
	if got := recorded.Header.Get("Authorization"); got != "Bearer access-token-1" {
		t.Fatalf("unexpected upstream auth header: %q", got)
	}
	if got := recorded.Header.Get("chatgpt-account-id"); got != "acct-123" {
		t.Fatalf("unexpected chatgpt-account-id header: %q", got)
	}
	if got := recorded.Header.Get("Accept"); got != "text/event-stream" {
		t.Fatalf("unexpected accept header: %q", got)
	}

	var upstreamBody map[string]any
	if err := json.Unmarshal(recorded.Body, &upstreamBody); err != nil {
		t.Fatalf("decode upstream body: %v", err)
	}
	if upstreamBody["instructions"] != "act as assistant" {
		t.Fatalf("unexpected instructions: %#v", upstreamBody["instructions"])
	}
	if upstreamBody["store"] != false {
		t.Fatalf("expected store=false, got %#v", upstreamBody["store"])
	}
	if upstreamBody["stream"] != true {
		t.Fatalf("expected stream=true, got %#v", upstreamBody["stream"])
	}
	input := upstreamBody["input"].([]any)
	if len(input) != 1 {
		t.Fatalf("expected 1 input item, got %d", len(input))
	}
	userItem := input[0].(map[string]any)
	if userItem["role"] != "user" {
		t.Fatalf("expected user role, got %#v", userItem["role"])
	}

	saved, err := loadState(statePath)
	if err != nil {
		t.Fatalf("load saved state: %v", err)
	}
	if saved.AccessToken != "access-token-1" {
		t.Fatalf("unexpected saved access token: %q", saved.AccessToken)
	}
}

func TestChatCompletionsStreamIncludesUsageAndDefaultInstructions(t *testing.T) {
	recorder := &upstreamRecorder{}
	upstream := newUpstreamServer(t, recorder, func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		recorder.add(r, body)
		writeResponsesSSE(
			w,
			`{"type":"response.created","response":{"id":"resp_stream","model":"gpt-5.4"}}`,
			`{"type":"response.output_text.delta","delta":"Hel"}`,
			`{"type":"response.output_text.delta","delta":"lo"}`,
			`{"type":"response.completed","response":{"id":"resp_stream","model":"gpt-5.4","status":"completed","output":[{"type":"message","role":"assistant","content":[{"type":"output_text","text":"Hello"}]}],"usage":{"input_tokens":2,"output_tokens":3,"input_tokens_details":{"cached_tokens":1}}}}`,
		)
	})
	defer upstream.Close()
	withTestUpstreamEndpoints(t, upstream.URL)

	server, _ := newTestLocalServer(t, validTestState())
	defer server.Close()

	reqBody := map[string]any{
		"model": "gpt-5.4",
		"messages": []map[string]any{
			{"role": "user", "content": "say hello"},
		},
		"stream": true,
		"stream_options": map[string]any{
			"include_usage": true,
		},
	}

	resp, err := http.Post(server.URL+"/v1/chat/completions", "application/json", marshalReader(t, reqBody))
	if err != nil {
		t.Fatalf("stream request failed: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("unexpected stream status: %d body=%s", resp.StatusCode, string(body))
	}

	streamBody, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read stream body: %v", err)
	}
	streamText := string(streamBody)
	if !strings.Contains(streamText, `"content":"Hel"`) || !strings.Contains(streamText, `"content":"lo"`) {
		t.Fatalf("expected content chunks in stream: %s", streamText)
	}
	if !strings.Contains(streamText, `"usage":{"prompt_tokens":2,"completion_tokens":3,"total_tokens":5,"prompt_tokens_details":{"cached_tokens":1}}`) {
		t.Fatalf("expected usage chunk in stream: %s", streamText)
	}
	if !strings.Contains(streamText, "data: [DONE]") {
		t.Fatalf("expected [DONE] sentinel in stream: %s", streamText)
	}

	recorded := recorder.lastRequest(t)
	var upstreamBody map[string]any
	if err := json.Unmarshal(recorded.Body, &upstreamBody); err != nil {
		t.Fatalf("decode upstream body: %v", err)
	}
	instructions, _ := upstreamBody["instructions"].(string)
	if strings.TrimSpace(instructions) == "" {
		t.Fatal("expected default instructions to be injected")
	}
	reasoning := upstreamBody["reasoning"].(map[string]any)
	if reasoning["effort"] != "low" {
		t.Fatalf("expected default reasoning effort low, got %#v", reasoning["effort"])
	}
}

func TestChatCompletionsImageModeTransformsToInputImage(t *testing.T) {
	recorder := &upstreamRecorder{}
	upstream := newUpstreamServer(t, recorder, func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		recorder.add(r, body)
		writeResponsesSSE(w, responsesCompletedEvent("resp_image", "gpt-5.4", "Image described"))
	})
	defer upstream.Close()
	withTestUpstreamEndpoints(t, upstream.URL)

	server, _ := newTestLocalServer(t, validTestState())
	defer server.Close()

	reqBody := map[string]any{
		"model": "gpt-5.4",
		"messages": []map[string]any{
			{
				"role": "user",
				"content": []map[string]any{
					{"type": "text", "text": "describe this image"},
					{
						"type": "image_url",
						"image_url": map[string]any{
							"url":    "data:image/png;base64,ZmFrZQ==",
							"detail": "high",
						},
					},
				},
			},
		},
		"stream": false,
	}

	_ = postJSON(t, server.URL+"/v1/chat/completions", reqBody, nil)

	recorded := recorder.lastRequest(t)
	var upstreamBody map[string]any
	if err := json.Unmarshal(recorded.Body, &upstreamBody); err != nil {
		t.Fatalf("decode upstream body: %v", err)
	}
	input := upstreamBody["input"].([]any)
	content := input[0].(map[string]any)["content"].([]any)
	foundImage := false
	for _, item := range content {
		part := item.(map[string]any)
		if part["type"] == "input_image" {
			foundImage = true
			break
		}
	}
	if !foundImage {
		t.Fatalf("expected input_image part, got %#v", content)
	}
}

func TestChatCompletionsForwardsToolsAndToolChoice(t *testing.T) {
	recorder := &upstreamRecorder{}
	upstream := newUpstreamServer(t, recorder, func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		recorder.add(r, body)
		writeResponsesSSE(w, responsesCompletedEvent("resp_tools", "gpt-5.4", "Tool schema accepted"))
	})
	defer upstream.Close()
	withTestUpstreamEndpoints(t, upstream.URL)

	server, _ := newTestLocalServer(t, validTestState())
	defer server.Close()

	reqBody := map[string]any{
		"model": "gpt-5.4",
		"messages": []map[string]any{
			{"role": "user", "content": "check weather"},
		},
		"stream": false,
		"tools": []map[string]any{
			{
				"type": "function",
				"function": map[string]any{
					"name":        "get_weather",
					"description": "Get weather",
					"parameters": map[string]any{
						"type": "object",
						"properties": map[string]any{
							"city": map[string]any{"type": "string"},
						},
						"required": []string{"city"},
					},
				},
			},
		},
		"tool_choice": "auto",
	}

	_ = postJSON(t, server.URL+"/v1/chat/completions", reqBody, nil)

	recorded := recorder.lastRequest(t)
	var upstreamBody map[string]any
	if err := json.Unmarshal(recorded.Body, &upstreamBody); err != nil {
		t.Fatalf("decode upstream body: %v", err)
	}
	tools, ok := upstreamBody["tools"].([]any)
	if !ok || len(tools) != 1 {
		t.Fatalf("expected 1 tool in upstream body, got %#v", upstreamBody["tools"])
	}
	tool := tools[0].(map[string]any)
	if tool["type"] != "function" {
		t.Fatalf("unexpected tool type: %#v", tool["type"])
	}
	if tool["name"] != "get_weather" {
		t.Fatalf("unexpected tool name: %#v", tool["name"])
	}
	if upstreamBody["tool_choice"] != "auto" {
		t.Fatalf("unexpected tool_choice: %#v", upstreamBody["tool_choice"])
	}
}

func TestChatCompletionsRefreshesExpiredToken(t *testing.T) {
	recorder := &upstreamRecorder{}
	mux := http.NewServeMux()
	mux.HandleFunc("/oauth/token", func(w http.ResponseWriter, r *http.Request) {
		recorder.incToken()
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"access_token":"refreshed-access","refresh_token":"refreshed-refresh","token_type":"Bearer","expires_in":3600}`)
	})
	mux.HandleFunc("/responses", func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		recorder.add(r, body)
		writeResponsesSSE(w, responsesCompletedEvent("resp_refresh", "gpt-5.4", "Refreshed ok"))
	})
	upstream := httptest.NewServer(mux)
	defer upstream.Close()
	withTestUpstreamEndpoints(t, upstream.URL)

	expired := validTestState()
	expired.AccessToken = "expired-access"
	expired.RefreshToken = "expired-refresh"
	expired.ExpiresAtUnix = time.Now().Add(-5 * time.Minute).Unix()
	server, statePath := newTestLocalServer(t, expired)
	defer server.Close()

	reqBody := map[string]any{
		"model": "gpt-5.4",
		"messages": []map[string]any{
			{"role": "user", "content": "hello"},
		},
		"stream": false,
	}
	_ = postJSON(t, server.URL+"/v1/chat/completions", reqBody, nil)

	if recorder.tokenCount() != 1 {
		t.Fatalf("expected 1 token refresh request, got %d", recorder.tokenCount())
	}
	recorded := recorder.lastRequest(t)
	if got := recorded.Header.Get("Authorization"); got != "Bearer refreshed-access" {
		t.Fatalf("expected refreshed access token upstream, got %q", got)
	}

	saved, err := loadState(statePath)
	if err != nil {
		t.Fatalf("load refreshed state: %v", err)
	}
	if saved.AccessToken != "refreshed-access" {
		t.Fatalf("expected refreshed access token in state file, got %q", saved.AccessToken)
	}
}

func TestResponsesEndpointAddsInstructionsAndConvertsStringInputToArray(t *testing.T) {
	recorder := &upstreamRecorder{}
	upstream := newUpstreamServer(t, recorder, func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		recorder.add(r, body)
		writeResponsesSSE(w, responsesCompletedEvent("resp_direct", "gpt-5.4", "Direct response"))
	})
	defer upstream.Close()
	withTestUpstreamEndpoints(t, upstream.URL)

	server, _ := newTestLocalServer(t, validTestState())
	defer server.Close()

	reqBody := map[string]any{
		"model":  "gpt-5.4",
		"input":  "hello direct responses",
		"stream": false,
	}

	respBody := postJSON(t, server.URL+"/v1/responses", reqBody, nil)
	var resp apicompat.ResponsesResponse
	if err := json.Unmarshal(respBody, &resp); err != nil {
		t.Fatalf("decode responses response: %v", err)
	}
	if resp.Status != "completed" {
		t.Fatalf("unexpected responses status: %s", resp.Status)
	}

	recorded := recorder.lastRequest(t)
	var upstreamBody map[string]any
	if err := json.Unmarshal(recorded.Body, &upstreamBody); err != nil {
		t.Fatalf("decode upstream body: %v", err)
	}
	instructions, _ := upstreamBody["instructions"].(string)
	if strings.TrimSpace(instructions) == "" {
		t.Fatal("expected instructions to be injected for /v1/responses")
	}
	input := upstreamBody["input"].([]any)
	if len(input) != 1 {
		t.Fatalf("expected converted input array, got %d items", len(input))
	}
	item := input[0].(map[string]any)
	if item["role"] != "user" {
		t.Fatalf("expected converted role=user, got %#v", item["role"])
	}
	reasoning := upstreamBody["reasoning"].(map[string]any)
	if reasoning["effort"] != "low" {
		t.Fatalf("expected default reasoning effort low, got %#v", reasoning["effort"])
	}
}

func TestChatCompletionsPreservesExplicitReasoningEffort(t *testing.T) {
	recorder := &upstreamRecorder{}
	upstream := newUpstreamServer(t, recorder, func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		recorder.add(r, body)
		writeResponsesSSE(w, responsesCompletedEvent("resp_reasoning", "gpt-5.4", "Reasoning ok"))
	})
	defer upstream.Close()
	withTestUpstreamEndpoints(t, upstream.URL)

	server, _ := newTestLocalServer(t, validTestState())
	defer server.Close()

	reqBody := map[string]any{
		"model": "gpt-5.4",
		"messages": []map[string]any{
			{"role": "user", "content": "hello"},
		},
		"stream":           false,
		"reasoning_effort": "high",
	}

	_ = postJSON(t, server.URL+"/v1/chat/completions", reqBody, nil)

	recorded := recorder.lastRequest(t)
	var upstreamBody map[string]any
	if err := json.Unmarshal(recorded.Body, &upstreamBody); err != nil {
		t.Fatalf("decode upstream body: %v", err)
	}
	reasoning := upstreamBody["reasoning"].(map[string]any)
	if reasoning["effort"] != "high" {
		t.Fatalf("expected explicit reasoning effort high, got %#v", reasoning["effort"])
	}
}

func newTestLocalServer(t *testing.T, state *savedState) (*httptest.Server, string) {
	t.Helper()
	dir := t.TempDir()
	statePath := filepath.Join(dir, "state.json")
	if err := saveState(statePath, state); err != nil {
		t.Fatalf("save state: %v", err)
	}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	server := &localOpenAIServer{
		stateFile: statePath,
		logger:    logger,
	}
	return httptest.NewServer(server.routes()), statePath
}

func validTestState() *savedState {
	return &savedState{
		Platform:         "openai",
		ClientID:         "app-test",
		RedirectURI:      "http://localhost:1455/auth/callback",
		AccessToken:      "access-token-1",
		RefreshToken:     "refresh-token-1",
		TokenType:        "Bearer",
		ExpiresAtUnix:    time.Now().Add(2 * time.Hour).Unix(),
		Email:            "tester@example.com",
		ChatGPTAccountID: "acct-123",
		OrganizationID:   "org-123",
		PlanType:         "plus",
		UpdatedAt:        time.Now().UTC(),
	}
}

func withTestUpstreamEndpoints(t *testing.T, baseURL string) {
	t.Helper()
	oldToken := tokenEndpointURL
	oldResponses := responsesEndpointURL
	tokenEndpointURL = baseURL + "/oauth/token"
	responsesEndpointURL = baseURL + "/responses"
	t.Cleanup(func() {
		tokenEndpointURL = oldToken
		responsesEndpointURL = oldResponses
	})
}

func newUpstreamServer(t *testing.T, recorder *upstreamRecorder, responsesHandler http.HandlerFunc) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/oauth/token", func(w http.ResponseWriter, r *http.Request) {
		recorder.incToken()
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"access_token":"access-token-1","refresh_token":"refresh-token-1","token_type":"Bearer","expires_in":3600}`)
	})
	mux.HandleFunc("/responses", responsesHandler)
	return httptest.NewServer(mux)
}

func responsesCompletedEvent(id, model, text string) string {
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

func writeResponsesSSE(w http.ResponseWriter, payloads ...string) {
	w.Header().Set("Content-Type", "text/event-stream")
	for _, payload := range payloads {
		_, _ = io.WriteString(w, "data: "+payload+"\n\n")
	}
	_, _ = io.WriteString(w, "data: [DONE]\n\n")
}

func postJSON(t *testing.T, url string, body any, headers map[string]string) []byte {
	t.Helper()
	req, err := http.NewRequest(http.MethodPost, url, marshalReader(t, body))
	if err != nil {
		t.Fatalf("create request: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	for key, value := range headers {
		req.Header.Set(key, value)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("post request failed: %v", err)
	}
	defer resp.Body.Close()
	respBody, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("unexpected status %d body=%s", resp.StatusCode, string(respBody))
	}
	return respBody
}

func marshalReader(t *testing.T, body any) io.Reader {
	t.Helper()
	payload, err := json.Marshal(body)
	if err != nil {
		t.Fatalf("marshal body: %v", err)
	}
	return bytes.NewReader(payload)
}

func stringContent(t *testing.T, raw json.RawMessage) string {
	t.Helper()
	var out string
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatalf("decode content string: %v", err)
	}
	return out
}

func TestChatStreamOutputCanBeScannedLineByLine(t *testing.T) {
	recorder := &upstreamRecorder{}
	upstream := newUpstreamServer(t, recorder, func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		recorder.add(r, body)
		writeResponsesSSE(
			w,
			`{"type":"response.created","response":{"id":"resp_scan","model":"gpt-5.4"}}`,
			`{"type":"response.output_text.delta","delta":"A"}`,
			`{"type":"response.completed","response":{"id":"resp_scan","model":"gpt-5.4","status":"completed","output":[{"type":"message","role":"assistant","content":[{"type":"output_text","text":"A"}]}]}}`,
		)
	})
	defer upstream.Close()
	withTestUpstreamEndpoints(t, upstream.URL)

	server, _ := newTestLocalServer(t, validTestState())
	defer server.Close()

	reqBody := map[string]any{
		"model": "gpt-5.4",
		"messages": []map[string]any{
			{"role": "user", "content": "A"},
		},
		"stream": true,
	}

	resp, err := http.Post(server.URL+"/v1/chat/completions", "application/json", marshalReader(t, reqBody))
	if err != nil {
		t.Fatalf("stream post failed: %v", err)
	}
	defer resp.Body.Close()

	scanner := bufio.NewScanner(resp.Body)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	var lines []string
	for scanner.Scan() {
		lines = append(lines, scanner.Text())
	}
	if err := scanner.Err(); err != nil {
		t.Fatalf("scan stream: %v", err)
	}
	joined := strings.Join(lines, "\n")
	if !strings.Contains(joined, `"chat.completion.chunk"`) {
		t.Fatalf("expected chat chunk output, got %s", joined)
	}
}
