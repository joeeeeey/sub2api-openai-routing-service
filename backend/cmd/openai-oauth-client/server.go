package main

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"strings"
	"time"

	apicompat "github.com/Wei-Shaw/sub2api/internal/pkg/apicompat"
	openaipkg "github.com/Wei-Shaw/sub2api/internal/pkg/openai"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/google/uuid"
)

type chatCompletionsCompatRequest struct {
	apicompat.ChatCompletionsRequest
	Metadata         map[string]any             `json:"metadata,omitempty"`
	ExtraBody        map[string]any             `json:"extra_body,omitempty"`
	WebSearchOptions map[string]any             `json:"web_search_options,omitempty"`
	LangfusePrompt   json.RawMessage            `json:"langfuse_prompt,omitempty"`
	ExtraHeaders     map[string]any             `json:"extra_headers,omitempty"`
	Unknown          map[string]json.RawMessage `json:"-"`
}

type responsesCompatRequest struct {
	apicompat.ResponsesRequest
	Metadata         map[string]any             `json:"metadata,omitempty"`
	ExtraBody        map[string]any             `json:"extra_body,omitempty"`
	WebSearchOptions map[string]any             `json:"web_search_options,omitempty"`
	LangfusePrompt   json.RawMessage            `json:"langfuse_prompt,omitempty"`
	Unknown          map[string]json.RawMessage `json:"-"`
}

type localOpenAIServer struct {
	stateFile string
	logger    *slog.Logger
}

type requestDebugInfo struct {
	RequestID                 string
	Path                      string
	Stream                    bool
	Model                     string
	MessageCount              int
	HasSystem                 bool
	HasImage                  bool
	ToolCount                 int
	TraceID                   string
	SessionID                 string
	ExtraUser                 string
	UnknownKeys               []string
	HasAuthHeader             bool
	Instructions              int
	PromptCacheKey            string
	DownstreamReasoningEffort string
	EffectiveReasoningEffort  string
	ReasoningEffortSource     string
}

type modelsListResponse struct {
	Object string            `json:"object"`
	Data   []openaipkg.Model `json:"data"`
}

func runServer(ctx context.Context, args []string) error {
	args = normalizeFlagAliases(args)
	fs := flag.NewFlagSet("server", flag.ContinueOnError)
	listenAddr := fs.String("listen", defaultServerListenAddr, "listen address for local OpenAI-compatible server")
	statePath := fs.String("state-file", defaultStateFile(), "oauth state file")
	logLevel := fs.String("log-level", "debug", "log level: debug|info")
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}

	state, err := loadState(*statePath)
	if err != nil {
		return fmt.Errorf("load state for server startup: %w", err)
	}

	logger := newServerLogger(*logLevel)
	server := &localOpenAIServer{
		stateFile: *statePath,
		logger:    logger,
	}

	logger.Info("starting local OpenAI-compatible OAuth server",
		slog.String("listen", *listenAddr),
		slog.String("state_file", *statePath),
		slog.String("email", emptyFallback(state.Email, "(unknown)")),
		slog.String("plan_type", emptyFallback(state.PlanType, "(unknown)")),
		slog.String("chatgpt_account_id", emptyFallback(state.ChatGPTAccountID, "(unknown)")),
		slog.Time("expires_at", time.Unix(state.ExpiresAtUnix, 0)),
		slog.Bool("token_expiring_soon", time.Unix(state.ExpiresAtUnix, 0).Before(time.Now().Add(refreshSkew))),
	)

	httpServer := &http.Server{
		Addr:              *listenAddr,
		Handler:           server.routes(),
		ReadHeaderTimeout: 10 * time.Second,
	}

	go func() {
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = httpServer.Shutdown(shutdownCtx)
	}()

	err = httpServer.ListenAndServe()
	if errors.Is(err, http.ErrServerClosed) {
		return nil
	}
	return err
}

func newServerLogger(level string) *slog.Logger {
	var slogLevel slog.Level
	switch strings.ToLower(strings.TrimSpace(level)) {
	case "info":
		slogLevel = slog.LevelInfo
	default:
		slogLevel = slog.LevelDebug
	}
	return slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{
		Level: slogLevel,
	}))
}

func (s *localOpenAIServer) routes() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", s.handleHealthz)
	mux.HandleFunc("/v1/models", s.handleModels)
	mux.HandleFunc("/v1/chat/completions", s.handleChatCompletions)
	mux.HandleFunc("/v1/responses", s.handleResponses)
	return mux
}

func (s *localOpenAIServer) handleHealthz(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeMethodNotAllowed(w, http.MethodGet)
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (s *localOpenAIServer) handleModels(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeMethodNotAllowed(w, http.MethodGet)
		return
	}
	writeJSON(w, http.StatusOK, modelsListResponse{
		Object: "list",
		Data:   openaipkg.DefaultModels,
	})
}

func (s *localOpenAIServer) handleChatCompletions(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeMethodNotAllowed(w, http.MethodPost)
		return
	}

	requestID := uuid.NewString()
	rawBody, err := io.ReadAll(io.LimitReader(r.Body, 8<<20))
	if err != nil {
		writeOpenAICompatError(w, http.StatusBadRequest, "invalid_request_error", "failed to read request body")
		return
	}

	debugInfo, req, upstreamBody, err := s.buildChatCompletionsUpstreamBody(requestID, rawBody, r)
	if err != nil {
		s.logger.Error("chat.completions request rejected",
			slog.String("request_id", requestID),
			slog.String("error", err.Error()),
		)
		writeOpenAICompatError(w, http.StatusBadRequest, "invalid_request_error", err.Error())
		return
	}

	state, err := loadState(s.stateFile)
	if err != nil {
		writeOpenAICompatError(w, http.StatusInternalServerError, "server_error", "failed to load oauth state")
		return
	}
	if err := ensureFreshToken(r.Context(), s.stateFile, state); err != nil {
		writeOpenAICompatError(w, http.StatusBadGateway, "upstream_error", "failed to refresh oauth token")
		return
	}

	upstreamReq, err := buildResponsesRequest(r.Context(), state, upstreamBody, true, "", "")
	if err != nil {
		writeOpenAICompatError(w, http.StatusInternalServerError, "server_error", "failed to build upstream request")
		return
	}

	start := time.Now()
	resp, err := (&http.Client{Timeout: 0}).Do(upstreamReq)
	if err != nil {
		s.logger.Error("chat.completions upstream request failed",
			slog.String("request_id", requestID),
			slog.String("error", err.Error()),
		)
		writeOpenAICompatError(w, http.StatusBadGateway, "upstream_error", "upstream request failed")
		return
	}
	defer resp.Body.Close()

	debugInfo.Stream = req.Stream
	s.logger.Info("chat.completions request accepted",
		slog.String("request_id", debugInfo.RequestID),
		slog.String("path", debugInfo.Path),
		slog.String("model", debugInfo.Model),
		slog.Bool("stream", debugInfo.Stream),
		slog.Int("messages", debugInfo.MessageCount),
		slog.Bool("has_system", debugInfo.HasSystem),
		slog.Bool("has_image", debugInfo.HasImage),
		slog.Int("tools", debugInfo.ToolCount),
		slog.Bool("has_auth_header", debugInfo.HasAuthHeader),
		slog.String("trace_id", debugInfo.TraceID),
		slog.String("session_id", debugInfo.SessionID),
		slog.String("extra_user", debugInfo.ExtraUser),
		slog.Any("unknown_keys", debugInfo.UnknownKeys),
		slog.Int("instructions_len", debugInfo.Instructions),
		slog.String("prompt_cache_key", debugInfo.PromptCacheKey),
		slog.String("downstream_reasoning_effort", debugInfo.DownstreamReasoningEffort),
		slog.String("effective_reasoning_effort", debugInfo.EffectiveReasoningEffort),
		slog.String("reasoning_effort_source", debugInfo.ReasoningEffortSource),
	)

	if resp.StatusCode >= 400 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 2<<20))
		msg := extractCompatErrorMessage(body)
		if msg == "" {
			msg = fmt.Sprintf("upstream returned status %d", resp.StatusCode)
		}
		s.logger.Error("chat.completions upstream returned error",
			slog.String("request_id", requestID),
			slog.Int("status", resp.StatusCode),
			slog.String("upstream_request_id", strings.TrimSpace(resp.Header.Get("x-request-id"))),
			slog.String("message", msg),
		)
		writeOpenAICompatError(w, resp.StatusCode, "upstream_error", msg)
		return
	}

	if req.Stream {
		s.handleChatCompletionsStream(w, resp, requestID, req, start)
		return
	}
	s.handleChatCompletionsBuffered(w, resp, requestID, req, start)
}

func (s *localOpenAIServer) handleResponses(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeMethodNotAllowed(w, http.MethodPost)
		return
	}

	requestID := uuid.NewString()
	rawBody, err := io.ReadAll(io.LimitReader(r.Body, 8<<20))
	if err != nil {
		writeOpenAICompatError(w, http.StatusBadRequest, "invalid_request_error", "failed to read request body")
		return
	}

	debugInfo, req, upstreamBody, err := s.buildResponsesUpstreamBody(requestID, rawBody, r)
	if err != nil {
		writeOpenAICompatError(w, http.StatusBadRequest, "invalid_request_error", err.Error())
		return
	}

	state, err := loadState(s.stateFile)
	if err != nil {
		writeOpenAICompatError(w, http.StatusInternalServerError, "server_error", "failed to load oauth state")
		return
	}
	if err := ensureFreshToken(r.Context(), s.stateFile, state); err != nil {
		writeOpenAICompatError(w, http.StatusBadGateway, "upstream_error", "failed to refresh oauth token")
		return
	}

	upstreamReq, err := buildResponsesRequest(r.Context(), state, upstreamBody, true, "", "")
	if err != nil {
		writeOpenAICompatError(w, http.StatusInternalServerError, "server_error", "failed to build upstream request")
		return
	}

	start := time.Now()
	resp, err := (&http.Client{Timeout: 0}).Do(upstreamReq)
	if err != nil {
		writeOpenAICompatError(w, http.StatusBadGateway, "upstream_error", "upstream request failed")
		return
	}
	defer resp.Body.Close()

	s.logger.Info("responses request accepted",
		slog.String("request_id", debugInfo.RequestID),
		slog.String("path", debugInfo.Path),
		slog.String("model", debugInfo.Model),
		slog.Bool("stream", debugInfo.Stream),
		slog.Int("instructions_len", debugInfo.Instructions),
		slog.Any("unknown_keys", debugInfo.UnknownKeys),
		slog.String("downstream_reasoning_effort", debugInfo.DownstreamReasoningEffort),
		slog.String("effective_reasoning_effort", debugInfo.EffectiveReasoningEffort),
		slog.String("reasoning_effort_source", debugInfo.ReasoningEffortSource),
	)

	if resp.StatusCode >= 400 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 2<<20))
		msg := extractCompatErrorMessage(body)
		if msg == "" {
			msg = fmt.Sprintf("upstream returned status %d", resp.StatusCode)
		}
		writeOpenAICompatError(w, resp.StatusCode, "upstream_error", msg)
		return
	}

	if req.Stream {
		if upstreamRequestID := strings.TrimSpace(resp.Header.Get("x-request-id")); upstreamRequestID != "" {
			w.Header().Set("x-request-id", upstreamRequestID)
		}
		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("Cache-Control", "no-cache")
		w.Header().Set("Connection", "keep-alive")
		w.WriteHeader(http.StatusOK)
		flusher, ok := w.(http.Flusher)
		if !ok {
			writeOpenAICompatError(w, http.StatusInternalServerError, "server_error", "streaming not supported")
			return
		}

		scanner := bufio.NewScanner(resp.Body)
		scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
		for scanner.Scan() {
			line := scanner.Text()
			if _, err := fmt.Fprintln(w, line); err != nil {
				return
			}
			flusher.Flush()
		}
		if err := scanner.Err(); err != nil && !errors.Is(err, context.Canceled) && !errors.Is(err, context.DeadlineExceeded) {
			s.logger.Warn("responses stream ended with error",
				slog.String("request_id", requestID),
				slog.String("error", err.Error()),
			)
		}
		s.logger.Info("responses stream completed",
			slog.String("request_id", requestID),
			slog.Duration("duration", time.Since(start)),
			slog.String("upstream_request_id", strings.TrimSpace(resp.Header.Get("x-request-id"))),
		)
		return
	}

	finalResp, usage, err := collectFinalResponsesObject(resp.Body)
	if err != nil {
		writeOpenAICompatError(w, http.StatusBadGateway, "upstream_error", "failed to read upstream response stream")
		return
	}
	if finalResp == nil {
		writeOpenAICompatError(w, http.StatusBadGateway, "upstream_error", "upstream stream ended without terminal response")
		return
	}
	if upstreamRequestID := strings.TrimSpace(resp.Header.Get("x-request-id")); upstreamRequestID != "" {
		w.Header().Set("x-request-id", upstreamRequestID)
	}
	writeJSON(w, http.StatusOK, finalResp)
	s.logger.Info("responses request completed",
		slog.String("request_id", requestID),
		slog.Duration("duration", time.Since(start)),
		slog.Int("input_tokens", usage.InputTokens),
		slog.Int("output_tokens", usage.OutputTokens),
		slog.String("upstream_request_id", strings.TrimSpace(resp.Header.Get("x-request-id"))),
	)
}

func (s *localOpenAIServer) handleChatCompletionsBuffered(w http.ResponseWriter, resp *http.Response, requestID string, req *chatCompletionsCompatRequest, start time.Time) {
	finalResp, usage, err := collectFinalResponsesObject(resp.Body)
	if err != nil {
		s.logger.Error("chat.completions buffered read failed",
			slog.String("request_id", requestID),
			slog.String("error", err.Error()),
		)
		writeOpenAICompatError(w, http.StatusBadGateway, "upstream_error", "failed to read upstream response stream")
		return
	}
	if finalResp == nil {
		writeOpenAICompatError(w, http.StatusBadGateway, "upstream_error", "upstream stream ended without terminal response")
		return
	}

	chatResp := apicompat.ResponsesToChatCompletions(finalResp, req.Model)
	if upstreamRequestID := strings.TrimSpace(resp.Header.Get("x-request-id")); upstreamRequestID != "" {
		w.Header().Set("x-request-id", upstreamRequestID)
	}
	writeJSON(w, http.StatusOK, chatResp)
	s.logger.Info("chat.completions buffered completed",
		slog.String("request_id", requestID),
		slog.Duration("duration", time.Since(start)),
		slog.Int("input_tokens", usage.InputTokens),
		slog.Int("output_tokens", usage.OutputTokens),
		slog.String("upstream_request_id", strings.TrimSpace(resp.Header.Get("x-request-id"))),
	)
}

func (s *localOpenAIServer) handleChatCompletionsStream(w http.ResponseWriter, resp *http.Response, requestID string, req *chatCompletionsCompatRequest, start time.Time) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		writeOpenAICompatError(w, http.StatusInternalServerError, "server_error", "streaming not supported")
		return
	}

	if upstreamRequestID := strings.TrimSpace(resp.Header.Get("x-request-id")); upstreamRequestID != "" {
		w.Header().Set("x-request-id", upstreamRequestID)
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.WriteHeader(http.StatusOK)

	state := apicompat.NewResponsesEventToChatState()
	state.Model = req.Model
	state.IncludeUsage = req.StreamOptions != nil && req.StreamOptions.IncludeUsage

	var usage openAIUsageSnapshot
	firstChunk := true
	var firstTokenMs int

	scanner := bufio.NewScanner(resp.Body)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)

	for scanner.Scan() {
		line := scanner.Text()
		if !strings.HasPrefix(line, "data: ") || line == "data: [DONE]" {
			continue
		}
		if firstChunk {
			firstChunk = false
			firstTokenMs = int(time.Since(start).Milliseconds())
		}

		payload := line[6:]
		var event apicompat.ResponsesStreamEvent
		if err := json.Unmarshal([]byte(payload), &event); err != nil {
			s.logger.Warn("chat.completions stream event parse failed",
				slog.String("request_id", requestID),
				slog.String("error", err.Error()),
			)
			continue
		}

		if (event.Type == "response.completed" || event.Type == "response.incomplete" || event.Type == "response.failed") && event.Response != nil && event.Response.Usage != nil {
			usage = usageFromResponsesUsage(event.Response.Usage)
		}

		chunks := apicompat.ResponsesEventToChatChunks(&event, state)
		for _, chunk := range chunks {
			sse, err := apicompat.ChatChunkToSSE(chunk)
			if err != nil {
				continue
			}
			if _, err := fmt.Fprint(w, sse); err != nil {
				s.logger.Info("chat.completions client disconnected",
					slog.String("request_id", requestID),
					slog.Int("first_token_ms", firstTokenMs),
				)
				return
			}
		}
		if len(chunks) > 0 {
			flusher.Flush()
		}
	}

	if err := scanner.Err(); err != nil && !errors.Is(err, context.Canceled) && !errors.Is(err, context.DeadlineExceeded) {
		s.logger.Warn("chat.completions stream ended with error",
			slog.String("request_id", requestID),
			slog.String("error", err.Error()),
		)
	}

	for _, chunk := range apicompat.FinalizeResponsesChatStream(state) {
		sse, err := apicompat.ChatChunkToSSE(chunk)
		if err != nil {
			continue
		}
		_, _ = fmt.Fprint(w, sse)
	}
	_, _ = fmt.Fprint(w, "data: [DONE]\n\n")
	flusher.Flush()

	s.logger.Info("chat.completions stream completed",
		slog.String("request_id", requestID),
		slog.Duration("duration", time.Since(start)),
		slog.Int("first_token_ms", firstTokenMs),
		slog.Int("input_tokens", usage.InputTokens),
		slog.Int("output_tokens", usage.OutputTokens),
		slog.String("upstream_request_id", strings.TrimSpace(resp.Header.Get("x-request-id"))),
	)
}

func (s *localOpenAIServer) buildChatCompletionsUpstreamBody(requestID string, rawBody []byte, r *http.Request) (*requestDebugInfo, *chatCompletionsCompatRequest, []byte, error) {
	var req chatCompletionsCompatRequest
	if err := json.Unmarshal(rawBody, &req); err != nil {
		return nil, nil, nil, fmt.Errorf("invalid chat.completions json: %w", err)
	}
	if strings.TrimSpace(req.Model) == "" {
		return nil, nil, nil, errors.New("model is required")
	}
	if len(req.Messages) == 0 {
		return nil, nil, nil, errors.New("messages is required")
	}

	rawTopLevel, unknownKeys := parseUnknownTopLevelKeys(rawBody, chatCompletionsKnownKeys)
	debugInfo := collectChatDebugInfo(requestID, "/v1/chat/completions", &req, unknownKeys, r)
	applyDefaultReasoningEffortToChatRequest(&req, debugInfo)

	responsesReq, err := apicompat.ChatCompletionsToResponses(&req.ChatCompletionsRequest)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("convert chat.completions to responses: %w", err)
	}

	body, err := json.Marshal(responsesReq)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("marshal responses body: %w", err)
	}

	var reqBody map[string]any
	if err := json.Unmarshal(body, &reqBody); err != nil {
		return nil, nil, nil, fmt.Errorf("decode responses body: %w", err)
	}

	transformResult := service.ApplyOpenAICodexOAuthTransform(reqBody, false, false)
	if transformResult.PromptCacheKey != "" {
		debugInfo.PromptCacheKey = transformResult.PromptCacheKey
	}
	if instructions, ok := reqBody["instructions"].(string); ok {
		debugInfo.Instructions = len(strings.TrimSpace(instructions))
	}
	if model, ok := reqBody["model"].(string); ok && strings.TrimSpace(model) != "" {
		debugInfo.Model = model
	}
	for _, passthroughKey := range []string{"metadata", "extra_body", "web_search_options", "langfuse_prompt"} {
		if raw, ok := rawTopLevel[passthroughKey]; ok && len(raw) > 0 {
			debugInfo.UnknownKeys = appendIfMissing(debugInfo.UnknownKeys, passthroughKey)
		}
	}

	finalBody, err := json.Marshal(reqBody)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("marshal transformed body: %w", err)
	}

	return debugInfo, &req, finalBody, nil
}

func (s *localOpenAIServer) buildResponsesUpstreamBody(requestID string, rawBody []byte, r *http.Request) (*requestDebugInfo, *responsesCompatRequest, []byte, error) {
	var req responsesCompatRequest
	if err := json.Unmarshal(rawBody, &req); err != nil {
		return nil, nil, nil, fmt.Errorf("invalid responses json: %w", err)
	}
	if strings.TrimSpace(req.Model) == "" {
		return nil, nil, nil, errors.New("model is required")
	}

	rawTopLevel, unknownKeys := parseUnknownTopLevelKeys(rawBody, responsesKnownKeys)
	debugInfo := &requestDebugInfo{
		RequestID:     requestID,
		Path:          "/v1/responses",
		Stream:        req.Stream,
		Model:         req.Model,
		TraceID:       stringMapValue(req.Metadata, "trace_id"),
		SessionID:     stringMapValue(req.Metadata, "session_id"),
		ExtraUser:     stringMapValue(req.ExtraBody, "user"),
		UnknownKeys:   unknownKeys,
		HasAuthHeader: strings.TrimSpace(r.Header.Get("Authorization")) != "",
	}
	applyDefaultReasoningEffortToResponsesRequest(&req, debugInfo)
	if instructionsRaw, ok := rawTopLevel["instructions"]; ok {
		var instructions string
		_ = json.Unmarshal(instructionsRaw, &instructions)
		debugInfo.Instructions = len(strings.TrimSpace(instructions))
	}

	bodyMap := make(map[string]any)
	if err := json.Unmarshal(rawBody, &bodyMap); err != nil {
		return nil, nil, nil, fmt.Errorf("decode responses body: %w", err)
	}
	if req.Reasoning != nil {
		reasoningMap := map[string]any{
			"effort": req.Reasoning.Effort,
		}
		if strings.TrimSpace(req.Reasoning.Summary) != "" {
			reasoningMap["summary"] = req.Reasoning.Summary
		}
		bodyMap["reasoning"] = reasoningMap
	}
	delete(bodyMap, "metadata")
	delete(bodyMap, "extra_body")
	delete(bodyMap, "web_search_options")
	delete(bodyMap, "langfuse_prompt")

	transformResult := service.ApplyOpenAICodexOAuthTransform(bodyMap, false, false)
	if transformResult.PromptCacheKey != "" {
		debugInfo.PromptCacheKey = transformResult.PromptCacheKey
	}
	if instructions, ok := bodyMap["instructions"].(string); ok {
		debugInfo.Instructions = len(strings.TrimSpace(instructions))
	}
	finalBody, err := json.Marshal(bodyMap)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("marshal transformed responses body: %w", err)
	}
	return debugInfo, &req, finalBody, nil
}

func collectFinalResponsesObject(body io.Reader) (*apicompat.ResponsesResponse, openAIUsageSnapshot, error) {
	scanner := bufio.NewScanner(body)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)

	var finalResponse *apicompat.ResponsesResponse
	var usage openAIUsageSnapshot
	for scanner.Scan() {
		line := scanner.Text()
		if !strings.HasPrefix(line, "data: ") || line == "data: [DONE]" {
			continue
		}
		payload := line[6:]
		var event apicompat.ResponsesStreamEvent
		if err := json.Unmarshal([]byte(payload), &event); err != nil {
			continue
		}
		if (event.Type == "response.completed" || event.Type == "response.incomplete" || event.Type == "response.failed") && event.Response != nil {
			finalResponse = event.Response
			if event.Response.Usage != nil {
				usage = usageFromResponsesUsage(event.Response.Usage)
			}
		}
	}
	if err := scanner.Err(); err != nil {
		return nil, usage, err
	}
	return finalResponse, usage, nil
}

type openAIUsageSnapshot struct {
	InputTokens  int
	OutputTokens int
	CachedTokens int
}

func usageFromResponsesUsage(usage *apicompat.ResponsesUsage) openAIUsageSnapshot {
	if usage == nil {
		return openAIUsageSnapshot{}
	}
	out := openAIUsageSnapshot{
		InputTokens:  usage.InputTokens,
		OutputTokens: usage.OutputTokens,
	}
	if usage.InputTokensDetails != nil {
		out.CachedTokens = usage.InputTokensDetails.CachedTokens
	}
	return out
}

func writeJSON(w http.ResponseWriter, statusCode int, payload any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(statusCode)
	_ = json.NewEncoder(w).Encode(payload)
}

func writeMethodNotAllowed(w http.ResponseWriter, allowedMethod string) {
	w.Header().Set("Allow", allowedMethod)
	writeOpenAICompatError(w, http.StatusMethodNotAllowed, "invalid_request_error", "method not allowed")
}

func writeOpenAICompatError(w http.ResponseWriter, statusCode int, errType, message string) {
	writeJSON(w, statusCode, map[string]any{
		"error": map[string]any{
			"type":    errType,
			"message": message,
		},
	})
}

func extractCompatErrorMessage(body []byte) string {
	var payload map[string]any
	if err := json.Unmarshal(body, &payload); err == nil {
		if detail, ok := payload["detail"].(string); ok && strings.TrimSpace(detail) != "" {
			return strings.TrimSpace(detail)
		}
		if errObj, ok := payload["error"].(map[string]any); ok {
			if msg, ok := errObj["message"].(string); ok && strings.TrimSpace(msg) != "" {
				return strings.TrimSpace(msg)
			}
		}
	}
	return strings.TrimSpace(string(body))
}

var chatCompletionsKnownKeys = map[string]struct{}{
	"model": {}, "messages": {}, "max_tokens": {}, "max_completion_tokens": {}, "temperature": {},
	"top_p": {}, "stream": {}, "stream_options": {}, "tools": {}, "tool_choice": {},
	"reasoning_effort": {}, "service_tier": {}, "stop": {}, "functions": {}, "function_call": {},
	"metadata": {}, "extra_body": {}, "web_search_options": {}, "langfuse_prompt": {}, "extra_headers": {},
}

var responsesKnownKeys = map[string]struct{}{
	"model": {}, "input": {}, "max_output_tokens": {}, "temperature": {}, "top_p": {}, "stream": {},
	"tools": {}, "include": {}, "store": {}, "reasoning": {}, "tool_choice": {}, "service_tier": {},
	"instructions": {}, "previous_response_id": {}, "metadata": {}, "extra_body": {}, "web_search_options": {},
	"langfuse_prompt": {},
}

func parseUnknownTopLevelKeys(rawBody []byte, known map[string]struct{}) (map[string]json.RawMessage, []string) {
	var payload map[string]json.RawMessage
	_ = json.Unmarshal(rawBody, &payload)
	out := make(map[string]json.RawMessage, len(payload))
	var unknown []string
	for key, value := range payload {
		out[key] = value
		if _, ok := known[key]; !ok {
			unknown = append(unknown, key)
		}
	}
	return out, unknown
}

func collectChatDebugInfo(requestID string, path string, req *chatCompletionsCompatRequest, unknownKeys []string, r *http.Request) *requestDebugInfo {
	info := &requestDebugInfo{
		RequestID:     requestID,
		Path:          path,
		Stream:        req.Stream,
		Model:         req.Model,
		MessageCount:  len(req.Messages),
		ToolCount:     len(req.Tools),
		TraceID:       stringMapValue(req.Metadata, "trace_id"),
		SessionID:     stringMapValue(req.Metadata, "session_id"),
		ExtraUser:     stringMapValue(req.ExtraBody, "user"),
		UnknownKeys:   unknownKeys,
		HasAuthHeader: strings.TrimSpace(r.Header.Get("Authorization")) != "",
	}

	for _, message := range req.Messages {
		if strings.EqualFold(message.Role, "system") {
			info.HasSystem = true
		}
		if chatMessageHasImage(message.Content) {
			info.HasImage = true
		}
	}
	return info
}

func applyDefaultReasoningEffortToChatRequest(req *chatCompletionsCompatRequest, debugInfo *requestDebugInfo) {
	if req == nil || debugInfo == nil {
		return
	}
	downstream := normalizeEffort(strings.TrimSpace(req.ReasoningEffort))
	source := "request"
	if downstream == "" {
		if nested := normalizeEffort(stringMapValue(req.ExtraBody, "reasoning_effort")); nested != "" {
			downstream = nested
			source = "extra_body"
		}
	}
	if downstream == "" {
		downstream = defaultEffort
		source = "default"
	}
	req.ReasoningEffort = downstream
	debugInfo.DownstreamReasoningEffort = normalizeEffort(strings.TrimSpace(req.ReasoningEffort))
	if source == "default" {
		debugInfo.DownstreamReasoningEffort = ""
	}
	debugInfo.EffectiveReasoningEffort = downstream
	debugInfo.ReasoningEffortSource = source
}

func applyDefaultReasoningEffortToResponsesRequest(req *responsesCompatRequest, debugInfo *requestDebugInfo) {
	if req == nil || debugInfo == nil {
		return
	}
	downstream := ""
	source := "request"
	if req.Reasoning != nil {
		downstream = normalizeEffort(strings.TrimSpace(req.Reasoning.Effort))
	}
	if downstream == "" {
		if nested := normalizeEffort(stringMapValue(req.ExtraBody, "reasoning_effort")); nested != "" {
			downstream = nested
			source = "extra_body"
		}
	}
	if downstream == "" {
		downstream = defaultEffort
		source = "default"
	}
	if req.Reasoning == nil {
		req.Reasoning = &apicompat.ResponsesReasoning{}
	}
	req.Reasoning.Effort = downstream
	if strings.TrimSpace(req.Reasoning.Summary) == "" {
		req.Reasoning.Summary = "auto"
	}
	if source != "default" && req.Reasoning != nil {
		debugInfo.DownstreamReasoningEffort = normalizeEffort(strings.TrimSpace(req.Reasoning.Effort))
	}
	debugInfo.EffectiveReasoningEffort = downstream
	debugInfo.ReasoningEffortSource = source
}

func chatMessageHasImage(content json.RawMessage) bool {
	var parts []map[string]any
	if err := json.Unmarshal(content, &parts); err != nil {
		return false
	}
	for _, part := range parts {
		if partType, _ := part["type"].(string); partType == "image_url" {
			return true
		}
	}
	return false
}

func stringMapValue(m map[string]any, key string) string {
	if m == nil {
		return ""
	}
	value, _ := m[key].(string)
	return strings.TrimSpace(value)
}

func appendIfMissing(in []string, value string) []string {
	for _, item := range in {
		if item == value {
			return in
		}
	}
	return append(in, value)
}
