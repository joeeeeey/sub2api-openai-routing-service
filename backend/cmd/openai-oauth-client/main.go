package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"syscall"
	"time"

	openaioauth "github.com/Wei-Shaw/sub2api/internal/pkg/openai"
)

const (
	defaultListenAddr       = ":1455"
	defaultServerListenAddr = "127.0.0.1:38080"
	defaultCallback         = "/auth/callback"
	defaultStateDir         = ".openai-oauth-client"
	defaultStateName        = "state.json"
	defaultModel            = "gpt-5.4"
	defaultEffort           = "low"
	refreshSkew             = 60 * time.Second
	userAgentCLI            = "codex_cli_rs/0.104.0"
	userAgentOAuth          = "codex-cli/0.91.0"
	originatorCLI           = "codex_cli_rs"
	defaultHTTPTimeout      = 120 * time.Second
)

var (
	tokenEndpointURL     = "https://auth.openai.com/oauth/token"
	responsesEndpointURL = "https://chatgpt.com/backend-api/codex/responses"
)

type savedState struct {
	Platform         string    `json:"platform"`
	ClientID         string    `json:"client_id"`
	RedirectURI      string    `json:"redirect_uri"`
	AccessToken      string    `json:"access_token"`
	RefreshToken     string    `json:"refresh_token"`
	IDToken          string    `json:"id_token,omitempty"`
	TokenType        string    `json:"token_type,omitempty"`
	ExpiresAtUnix    int64     `json:"expires_at_unix"`
	Email            string    `json:"email,omitempty"`
	ChatGPTAccountID string    `json:"chatgpt_account_id,omitempty"`
	ChatGPTUserID    string    `json:"chatgpt_user_id,omitempty"`
	OrganizationID   string    `json:"organization_id,omitempty"`
	PlanType         string    `json:"plan_type,omitempty"`
	UpdatedAt        time.Time `json:"updated_at"`
}

type tokenResponse struct {
	AccessToken  string `json:"access_token"`
	IDToken      string `json:"id_token"`
	TokenType    string `json:"token_type"`
	ExpiresIn    int64  `json:"expires_in"`
	RefreshToken string `json:"refresh_token,omitempty"`
	Scope        string `json:"scope,omitempty"`
}

type callbackResult struct {
	code  string
	state string
	err   error
}

type responsesRequest struct {
	Instructions string               `json:"instructions,omitempty"`
	Model        string               `json:"model"`
	Input        []responsesInputItem `json:"input"`
	Stream       bool                 `json:"stream,omitempty"`
	Store        *bool                `json:"store,omitempty"`
	Reasoning    *responsesReasoning  `json:"reasoning,omitempty"`
}

type responsesReasoning struct {
	Effort  string `json:"effort,omitempty"`
	Summary string `json:"summary,omitempty"`
}

type responsesInputItem struct {
	Role    string                 `json:"role,omitempty"`
	Content []responsesContentPart `json:"content,omitempty"`
}

type responsesContentPart struct {
	Type string `json:"type"`
	Text string `json:"text,omitempty"`
}

type responsesResponse struct {
	ID     string            `json:"id"`
	Object string            `json:"object"`
	Model  string            `json:"model"`
	Status string            `json:"status"`
	Output []responsesOutput `json:"output"`
	Error  *responsesError   `json:"error,omitempty"`
}

type responsesError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

type responsesOutput struct {
	Type    string                 `json:"type"`
	Role    string                 `json:"role,omitempty"`
	Content []responsesContentPart `json:"content,omitempty"`
	Summary []responsesSummary     `json:"summary,omitempty"`
}

type responsesSummary struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

type streamEvent struct {
	Type     string          `json:"type"`
	Delta    string          `json:"delta,omitempty"`
	Response json.RawMessage `json:"response,omitempty"`
	Error    *responsesError `json:"error,omitempty"`
}

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	if len(os.Args) < 2 {
		usageAndExit(1)
	}

	switch os.Args[1] {
	case "login":
		if err := runLogin(ctx, os.Args[2:]); err != nil {
			fatalf("login failed: %v", err)
		}
	case "chat", "responses":
		if err := runChat(ctx, os.Args[2:]); err != nil {
			fatalf("chat failed: %v", err)
		}
	case "server":
		if err := runServer(ctx, os.Args[2:]); err != nil {
			fatalf("server failed: %v", err)
		}
	case "show-state":
		if err := runShowState(os.Args[2:]); err != nil {
			fatalf("show-state failed: %v", err)
		}
	case "logout":
		if err := runLogout(os.Args[2:]); err != nil {
			fatalf("logout failed: %v", err)
		}
	case "-h", "--help", "help":
		usageAndExit(0)
	default:
		usageAndExit(1)
	}
}

func runLogin(ctx context.Context, args []string) error {
	args = normalizeFlagAliases(args)
	fs := flag.NewFlagSet("login", flag.ContinueOnError)
	listenAddr := fs.String("listen", defaultListenAddr, "local listen address for oauth callback")
	redirectURIFlag := fs.String("redirect-uri", openaioauth.DefaultRedirectURI, "oauth redirect uri")
	callbackPath := fs.String("callback-path", "", "oauth callback path; default derives from redirect-uri")
	statePath := fs.String("state-file", defaultStateFile(), "file used to save oauth state")
	platform := fs.String("platform", openaioauth.OAuthPlatformOpenAI, "oauth platform: openai or sora")
	openBrowser := fs.Bool("open-browser", true, "open the authorization URL automatically")
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}

	redirectURI := strings.TrimSpace(*redirectURIFlag)
	if redirectURI == "" {
		redirectURI = openaioauth.DefaultRedirectURI
	}
	callbackPathValue, err := resolveCallbackPath(redirectURI, *callbackPath)
	if err != nil {
		return err
	}
	state, err := openaioauth.GenerateState()
	if err != nil {
		return fmt.Errorf("generate state: %w", err)
	}
	codeVerifier, err := openaioauth.GenerateCodeVerifier()
	if err != nil {
		return fmt.Errorf("generate code verifier: %w", err)
	}
	codeChallenge := openaioauth.GenerateCodeChallenge(codeVerifier)
	clientID, _ := openaioauth.OAuthClientConfigByPlatform(*platform)
	authURL := openaioauth.BuildAuthorizationURLForPlatform(state, codeChallenge, redirectURI, *platform)

	resultCh := make(chan callbackResult, 1)
	serverErrCh := make(chan error, 1)

	srv, err := startCallbackServer(*listenAddr, callbackPathValue, state, resultCh, serverErrCh)
	if err != nil {
		return err
	}
	defer func() {
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		_ = srv.Shutdown(shutdownCtx)
	}()

	fmt.Fprintf(os.Stderr, "OAuth authorize URL:\n%s\n\n", authURL)
	if *openBrowser {
		if err := openURL(authURL); err != nil {
			fmt.Fprintf(os.Stderr, "warning: failed to open browser automatically: %v\n", err)
			fmt.Fprintln(os.Stderr, "open the URL manually in your browser")
		}
	}
	fmt.Fprintf(os.Stderr, "waiting for callback on %s%s ...\n", *listenAddr, callbackPathValue)

	select {
	case <-ctx.Done():
		return ctx.Err()
	case err := <-serverErrCh:
		return err
	case result := <-resultCh:
		if result.err != nil {
			return result.err
		}
		if subtleTrimCompare(result.state, state) == false {
			return errors.New("oauth state mismatch")
		}
		token, err := exchangeCode(ctx, result.code, codeVerifier, redirectURI, clientID)
		if err != nil {
			return err
		}
		saved, err := buildSavedState(*platform, clientID, redirectURI, token)
		if err != nil {
			return err
		}
		if err := saveState(*statePath, saved); err != nil {
			return err
		}
		fmt.Fprintf(os.Stderr, "saved oauth state to %s\n", *statePath)
		printSavedState(saved)
		return nil
	}
}

func runChat(ctx context.Context, args []string) error {
	args = normalizeFlagAliases(args)
	fs := flag.NewFlagSet("chat", flag.ContinueOnError)
	prompt := fs.String("prompt", "", "prompt text")
	model := fs.String("model", defaultModel, "responses model name")
	thinkingEffort := fs.String("thinking-effort", defaultEffort, "reasoning effort: none|low|medium|high")
	instructions := fs.String("instructions", strings.TrimSpace(openaioauth.DefaultInstructions), "instructions sent to ChatGPT Codex responses API")
	stream := fs.Bool("stream", true, "enable streaming mode")
	statePath := fs.String("state-file", defaultStateFile(), "oauth state file")
	sessionID := fs.String("session-id", "", "optional session_id header")
	conversationID := fs.String("conversation-id", "", "optional conversation_id header")
	raw := fs.Bool("raw", false, "print raw JSON / raw SSE data")
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}
	if strings.TrimSpace(*prompt) == "" {
		return errors.New("--prompt is required")
	}

	state, err := loadState(*statePath)
	if err != nil {
		return err
	}
	if err := ensureFreshToken(ctx, *statePath, state); err != nil {
		return err
	}

	reqBody, err := buildResponsesBody(
		*model,
		strings.TrimSpace(*prompt),
		*stream,
		strings.TrimSpace(*thinkingEffort),
		strings.TrimSpace(*instructions),
	)
	if err != nil {
		return err
	}

	if *stream {
		return streamResponses(ctx, state, reqBody, *sessionID, *conversationID, *raw)
	}
	return requestResponses(ctx, state, reqBody, *sessionID, *conversationID, *raw)
}

func runShowState(args []string) error {
	args = normalizeFlagAliases(args)
	fs := flag.NewFlagSet("show-state", flag.ContinueOnError)
	statePath := fs.String("state-file", defaultStateFile(), "oauth state file")
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}
	state, err := loadState(*statePath)
	if err != nil {
		return err
	}
	printSavedState(state)
	return nil
}

func runLogout(args []string) error {
	args = normalizeFlagAliases(args)
	fs := flag.NewFlagSet("logout", flag.ContinueOnError)
	statePath := fs.String("state-file", defaultStateFile(), "oauth state file")
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}
	if err := os.Remove(*statePath); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	fmt.Fprintf(os.Stderr, "removed %s\n", *statePath)
	return nil
}

func startCallbackServer(listenAddr, callbackPath, expectedState string, resultCh chan<- callbackResult, errCh chan<- error) (*http.Server, error) {
	mux := http.NewServeMux()
	var once sync.Once
	sendResult := func(result callbackResult) {
		once.Do(func() {
			resultCh <- result
		})
	}

	mux.HandleFunc(callbackPath, func(w http.ResponseWriter, r *http.Request) {
		if !isLoopbackRemoteAddr(r.RemoteAddr) {
			http.Error(w, "forbidden", http.StatusForbidden)
			return
		}
		query := r.URL.Query()
		if errText := strings.TrimSpace(query.Get("error")); errText != "" {
			msg := errText
			if desc := strings.TrimSpace(query.Get("error_description")); desc != "" {
				msg += ": " + desc
			}
			http.Error(w, msg, http.StatusBadRequest)
			sendResult(callbackResult{err: errors.New(msg)})
			return
		}

		code := strings.TrimSpace(query.Get("code"))
		state := strings.TrimSpace(query.Get("state"))
		if code == "" {
			http.Error(w, "missing code", http.StatusBadRequest)
			sendResult(callbackResult{err: errors.New("oauth callback missing code")})
			return
		}
		if !subtleTrimCompare(state, expectedState) {
			http.Error(w, "state mismatch", http.StatusBadRequest)
			sendResult(callbackResult{err: errors.New("oauth callback state mismatch")})
			return
		}

		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = io.WriteString(w, `<!doctype html><html><body><h2>OpenAI OAuth completed</h2><p>You can return to the terminal now.</p></body></html>`)
		sendResult(callbackResult{code: code, state: state})
	})

	ln, err := net.Listen("tcp", listenAddr)
	if err != nil {
		return nil, fmt.Errorf("listen %s: %w", listenAddr, err)
	}

	srv := &http.Server{
		Handler:           mux,
		ReadHeaderTimeout: 5 * time.Second,
	}

	go func() {
		if serveErr := srv.Serve(ln); serveErr != nil && !errors.Is(serveErr, http.ErrServerClosed) {
			errCh <- serveErr
		}
	}()

	return srv, nil
}

func exchangeCode(ctx context.Context, code, codeVerifier, redirectURI, clientID string) (*tokenResponse, error) {
	form := url.Values{}
	form.Set("grant_type", "authorization_code")
	form.Set("client_id", clientID)
	form.Set("code", code)
	form.Set("redirect_uri", redirectURI)
	form.Set("code_verifier", codeVerifier)
	return doTokenRequest(ctx, form)
}

func refreshAccessToken(ctx context.Context, refreshToken, clientID string) (*tokenResponse, error) {
	form := url.Values{}
	form.Set("grant_type", "refresh_token")
	form.Set("refresh_token", refreshToken)
	form.Set("client_id", clientID)
	form.Set("scope", openaioauth.RefreshScopes)
	return doTokenRequest(ctx, form)
}

func doTokenRequest(ctx context.Context, form url.Values) (*tokenResponse, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, tokenEndpointURL, strings.NewReader(form.Encode()))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("User-Agent", userAgentOAuth)

	client := &http.Client{Timeout: defaultHTTPTimeout}
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("oauth token request failed: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return nil, err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("oauth token request failed: status=%d body=%s", resp.StatusCode, strings.TrimSpace(string(body)))
	}

	var token tokenResponse
	if err := json.Unmarshal(body, &token); err != nil {
		return nil, fmt.Errorf("decode oauth token response: %w", err)
	}
	if strings.TrimSpace(token.AccessToken) == "" {
		return nil, errors.New("oauth token response missing access_token")
	}
	return &token, nil
}

func buildSavedState(platform, clientID, redirectURI string, token *tokenResponse) (*savedState, error) {
	saved := &savedState{
		Platform:      platform,
		ClientID:      strings.TrimSpace(clientID),
		RedirectURI:   strings.TrimSpace(redirectURI),
		AccessToken:   strings.TrimSpace(token.AccessToken),
		RefreshToken:  strings.TrimSpace(token.RefreshToken),
		IDToken:       strings.TrimSpace(token.IDToken),
		TokenType:     strings.TrimSpace(token.TokenType),
		ExpiresAtUnix: time.Now().Add(time.Duration(token.ExpiresIn) * time.Second).Unix(),
		UpdatedAt:     time.Now().UTC(),
	}

	if saved.IDToken != "" {
		claims, err := openaioauth.ParseIDToken(saved.IDToken)
		if err == nil {
			info := claims.GetUserInfo()
			saved.Email = strings.TrimSpace(info.Email)
			saved.ChatGPTAccountID = strings.TrimSpace(info.ChatGPTAccountID)
			saved.ChatGPTUserID = strings.TrimSpace(info.ChatGPTUserID)
			saved.OrganizationID = strings.TrimSpace(info.OrganizationID)
			saved.PlanType = strings.TrimSpace(info.PlanType)
		}
	}

	return saved, nil
}

func ensureFreshToken(ctx context.Context, statePath string, state *savedState) error {
	if state == nil {
		return errors.New("missing oauth state")
	}
	if strings.TrimSpace(state.AccessToken) == "" {
		return errors.New("oauth state missing access_token")
	}
	if time.Unix(state.ExpiresAtUnix, 0).After(time.Now().Add(refreshSkew)) {
		return nil
	}
	if strings.TrimSpace(state.RefreshToken) == "" {
		return errors.New("oauth access token expired and refresh_token is missing; run login again")
	}

	clientID := strings.TrimSpace(state.ClientID)
	if clientID == "" {
		clientID, _ = openaioauth.OAuthClientConfigByPlatform(state.Platform)
	}

	token, err := refreshAccessToken(ctx, state.RefreshToken, clientID)
	if err != nil {
		return err
	}
	updated, err := buildSavedState(state.Platform, clientID, state.RedirectURI, token)
	if err != nil {
		return err
	}
	if updated.ChatGPTAccountID == "" {
		updated.ChatGPTAccountID = state.ChatGPTAccountID
	}
	if updated.OrganizationID == "" {
		updated.OrganizationID = state.OrganizationID
	}
	if updated.Email == "" {
		updated.Email = state.Email
	}
	*state = *updated
	return saveState(statePath, state)
}

func buildResponsesBody(model, prompt string, stream bool, effort string, instructions string) ([]byte, error) {
	model = normalizeModel(model)
	effort = normalizeEffort(effort)
	instructions = strings.TrimSpace(instructions)
	store := false

	req := responsesRequest{
		Instructions: instructions,
		Model:        model,
		Stream:       stream,
		Store:        &store,
		Input: []responsesInputItem{
			{
				Role: "user",
				Content: []responsesContentPart{
					{Type: "input_text", Text: prompt},
				},
			},
		},
	}
	if effort != "" {
		req.Reasoning = &responsesReasoning{
			Effort:  effort,
			Summary: "auto",
		}
	}
	return json.Marshal(req)
}

func streamResponses(ctx context.Context, state *savedState, reqBody []byte, sessionID, conversationID string, raw bool) error {
	req, err := buildResponsesRequest(ctx, state, reqBody, true, sessionID, conversationID)
	if err != nil {
		return err
	}

	client := &http.Client{Timeout: 0}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return readHTTPError(resp)
	}

	scanner := bufio.NewScanner(resp.Body)
	buf := make([]byte, 0, 64*1024)
	scanner.Buffer(buf, 1024*1024)

	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || !strings.HasPrefix(line, "data:") {
			continue
		}
		payload := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		if payload == "[DONE]" {
			if !raw {
				fmt.Println()
			}
			return nil
		}
		if raw {
			fmt.Println(payload)
			continue
		}
		var event streamEvent
		if err := json.Unmarshal([]byte(payload), &event); err != nil {
			continue
		}
		switch event.Type {
		case "response.output_text.delta":
			if event.Delta != "" {
				fmt.Print(event.Delta)
			}
		case "response.completed":
			fmt.Println()
			return nil
		case "response.failed", "error":
			if event.Error != nil && event.Error.Message != "" {
				return errors.New(event.Error.Message)
			}
			return fmt.Errorf("stream failed: %s", payload)
		}
	}
	if err := scanner.Err(); err != nil {
		return err
	}
	return nil
}

func requestResponses(ctx context.Context, state *savedState, reqBody []byte, sessionID, conversationID string, raw bool) error {
	req, err := buildResponsesRequest(ctx, state, reqBody, false, sessionID, conversationID)
	if err != nil {
		return err
	}

	client := &http.Client{Timeout: defaultHTTPTimeout}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return readHTTPError(resp)
	}

	body, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return err
	}
	if raw {
		fmt.Println(string(body))
		return nil
	}

	var decoded responsesResponse
	if err := json.Unmarshal(body, &decoded); err != nil {
		return err
	}
	if decoded.Error != nil && decoded.Error.Message != "" {
		return errors.New(decoded.Error.Message)
	}
	text := extractOutputText(decoded.Output)
	if text == "" {
		fmt.Println(string(body))
		return nil
	}
	fmt.Println(text)
	return nil
}

func buildResponsesRequest(ctx context.Context, state *savedState, body []byte, stream bool, sessionID, conversationID string) (*http.Request, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, responsesEndpointURL, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Host = "chatgpt.com"
	req.Header.Set("Authorization", "Bearer "+state.AccessToken)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", userAgentCLI)
	req.Header.Set("OpenAI-Beta", "responses=experimental")
	req.Header.Set("originator", originatorCLI)
	if stream {
		req.Header.Set("Accept", "text/event-stream")
	} else {
		req.Header.Set("Accept", "application/json")
	}
	if strings.TrimSpace(state.ChatGPTAccountID) != "" {
		req.Header.Set("chatgpt-account-id", strings.TrimSpace(state.ChatGPTAccountID))
	}
	if strings.TrimSpace(sessionID) != "" {
		req.Header.Set("session_id", strings.TrimSpace(sessionID))
	}
	if strings.TrimSpace(conversationID) != "" {
		req.Header.Set("conversation_id", strings.TrimSpace(conversationID))
	}
	return req, nil
}

func extractOutputText(outputs []responsesOutput) string {
	var parts []string
	for _, output := range outputs {
		switch output.Type {
		case "message":
			for _, part := range output.Content {
				if (part.Type == "output_text" || part.Type == "text") && strings.TrimSpace(part.Text) != "" {
					parts = append(parts, part.Text)
				}
			}
		case "reasoning":
			for _, summary := range output.Summary {
				if summary.Type == "summary_text" && strings.TrimSpace(summary.Text) != "" {
					parts = append(parts, summary.Text)
				}
			}
		}
	}
	return strings.Join(parts, "")
}

func readHTTPError(resp *http.Response) error {
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 2<<20))
	trimmed := strings.TrimSpace(string(body))
	if trimmed == "" {
		return fmt.Errorf("http %d", resp.StatusCode)
	}
	return fmt.Errorf("http %d: %s", resp.StatusCode, trimmed)
}

func loadState(path string) (*savedState, error) {
	body, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var state savedState
	if err := json.Unmarshal(body, &state); err != nil {
		return nil, err
	}
	if strings.TrimSpace(state.Platform) == "" {
		state.Platform = openaioauth.OAuthPlatformOpenAI
	}
	return &state, nil
}

func saveState(path string, state *savedState) error {
	if state == nil {
		return errors.New("nil state")
	}
	state.UpdatedAt = time.Now().UTC()
	body, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, body, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

func printSavedState(state *savedState) {
	fmt.Printf("email=%s\n", emptyFallback(state.Email, "(unknown)"))
	fmt.Printf("plan_type=%s\n", emptyFallback(state.PlanType, "(unknown)"))
	fmt.Printf("chatgpt_account_id=%s\n", emptyFallback(state.ChatGPTAccountID, "(unknown)"))
	fmt.Printf("organization_id=%s\n", emptyFallback(state.OrganizationID, "(unknown)"))
	fmt.Printf("expires_at=%s\n", time.Unix(state.ExpiresAtUnix, 0).Format(time.RFC3339))
}

func openURL(rawURL string) error {
	switch runtime.GOOS {
	case "darwin":
		return exec.Command("open", rawURL).Run()
	case "linux":
		return exec.Command("xdg-open", rawURL).Run()
	case "windows":
		return exec.Command("rundll32", "url.dll,FileProtocolHandler", rawURL).Run()
	default:
		return fmt.Errorf("unsupported OS for auto-open: %s", runtime.GOOS)
	}
}

func buildRedirectURI(listenAddr, callbackPath string) string {
	if !strings.HasPrefix(callbackPath, "/") {
		callbackPath = "/" + callbackPath
	}
	return "http://" + listenAddr + callbackPath
}

func resolveCallbackPath(redirectURI, callbackPath string) (string, error) {
	if trimmed := strings.TrimSpace(callbackPath); trimmed != "" {
		if !strings.HasPrefix(trimmed, "/") {
			return "", errors.New("callback path must start with /")
		}
		return trimmed, nil
	}
	parsed, err := url.Parse(strings.TrimSpace(redirectURI))
	if err != nil {
		return "", fmt.Errorf("parse redirect-uri: %w", err)
	}
	if strings.TrimSpace(parsed.Path) == "" {
		return defaultCallback, nil
	}
	return parsed.Path, nil
}

func normalizeModel(model string) string {
	trimmed := strings.ToLower(strings.TrimSpace(model))
	switch trimmed {
	case "", "gpt5-4", "gpt5.4", "gpt-5-4":
		return defaultModel
	default:
		return strings.TrimSpace(model)
	}
}

func normalizeEffort(effort string) string {
	switch strings.ToLower(strings.TrimSpace(effort)) {
	case "", "default":
		return defaultEffort
	case "minimal":
		return "none"
	case "none", "low", "medium", "high":
		return strings.ToLower(strings.TrimSpace(effort))
	default:
		return strings.ToLower(strings.TrimSpace(effort))
	}
}

func subtleTrimCompare(a, b string) bool {
	return strings.TrimSpace(a) == strings.TrimSpace(b)
}

func isLoopbackRemoteAddr(remoteAddr string) bool {
	host, _, err := net.SplitHostPort(strings.TrimSpace(remoteAddr))
	if err != nil {
		host = strings.TrimSpace(remoteAddr)
	}
	ip := net.ParseIP(host)
	if ip == nil {
		return strings.EqualFold(host, "localhost")
	}
	return ip.IsLoopback()
}

func emptyFallback(value, fallback string) string {
	if strings.TrimSpace(value) == "" {
		return fallback
	}
	return value
}

func defaultStateFile() string {
	root := detectProjectRoot()
	return filepath.Join(root, defaultStateDir, defaultStateName)
}

func normalizeFlagAliases(args []string) []string {
	replacer := strings.NewReplacer(
		"--state_file", "--state-file",
		"--callback_path", "--callback-path",
		"--open_browser", "--open-browser",
		"--thinking_effort", "--thinking-effort",
		"--thinking_effot", "--thinking-effort",
		"--session_id", "--session-id",
		"--conversation_id", "--conversation-id",
	)
	out := make([]string, 0, len(args))
	for _, arg := range args {
		out = append(out, replacer.Replace(arg))
	}
	return out
}

func detectProjectRoot() string {
	cwd, err := os.Getwd()
	if err != nil {
		return "."
	}
	current := cwd
	for {
		if fileExists(filepath.Join(current, ".git")) || fileExists(filepath.Join(current, "backend", "go.mod")) {
			return current
		}
		if filepath.Base(current) == "backend" && fileExists(filepath.Join(current, "go.mod")) {
			return filepath.Dir(current)
		}
		parent := filepath.Dir(current)
		if parent == current {
			return cwd
		}
		current = parent
	}
}

func fileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

func usageAndExit(code int) {
	_, _ = fmt.Fprintf(os.Stderr, `Usage:
  openai-oauth-client login [--state-file PATH]
  openai-oauth-client chat --prompt "hello" [--model gpt-5.4] [--thinking-effort low] [--stream=true]
  openai-oauth-client server [--listen 127.0.0.1:38080]
  openai-oauth-client show-state
  openai-oauth-client logout

Default state file:
  %s

Examples:
  bash scripts/openai-oauth-client.sh login
  bash scripts/openai-oauth-client.sh server
  bash scripts/openai-oauth-client.sh chat --stream=true --prompt "hello" --model gpt-5.4 --thinking-effort low
  go run ./cmd/openai-oauth-client login
  go run ./cmd/openai-oauth-client server
  go run ./cmd/openai-oauth-client chat --stream=true --prompt "hello" --model gpt-5.4 --thinking-effort low
`, defaultStateFile())
	os.Exit(code)
}

func fatalf(format string, args ...any) {
	_, _ = fmt.Fprintf(os.Stderr, format+"\n", args...)
	os.Exit(1)
}
