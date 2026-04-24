package service

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"net/http"
	"regexp"
	"strings"

	"github.com/tidwall/gjson"
)

const (
	ccRewriteSalt         = "59cf53e54c78"
	ccRewriteFallbackHash = "000"
)

var (
	ccRewriteHashPositions = []int{4, 7, 20}

	ccRewriteBillingBlockPattern  = regexp.MustCompile(`^\s*x-anthropic-billing-header:`)
	ccRewriteBillingInlinePattern = regexp.MustCompile(`x-anthropic-billing-header:[^\n]+\n?`)
	ccRewriteSystemReminderRe     = regexp.MustCompile(`(?s)(<system-reminder>)(.*?)(</system-reminder>)`)
	ccRewriteCCVersionPattern     = regexp.MustCompile(`cc_version=[\d.]+\.[a-f0-9]{3}`)
	ccRewritePlatformPattern      = regexp.MustCompile(`Platform:\s*\S+`)
	ccRewriteShellPattern         = regexp.MustCompile(`Shell:\s*\S+`)
	ccRewriteOSVersionPattern     = regexp.MustCompile(`OS Version:\s*[^\n<]+`)
	ccRewriteWorkingDirPattern    = regexp.MustCompile(`((?:Primary )?[Ww]orking directory:\s*)\/\S+`)
	ccRewriteHomePrefixPattern    = regexp.MustCompile(`\/(?:Users|home)\/[^\/\s]+\/`)
)

type ClaudeCodeRewriteProfile struct {
	DeviceID   string
	Version    string
	UserAgent  string
	Platform   string
	Shell      string
	OSVersion  string
	WorkingDir string
}

func BuildClaudeCodeRewriteProfile(account *Account, fp *Fingerprint, headers http.Header) ClaudeCodeRewriteProfile {
	version := strings.TrimSpace(accountExtraString(account, "cc_rewrite_version"))
	if version == "" {
		version = ExtractCLIVersion(resolveRewriteUserAgent(fp, headers))
	}
	if version == "" {
		version = ExtractCLIVersion(defaultFingerprint.UserAgent)
	}
	if version == "" {
		version = "2.1.22"
	}

	deviceID := ""
	if fp != nil {
		deviceID = strings.TrimSpace(fp.ClientID)
	}
	if deviceID == "" && account != nil {
		deviceID = strings.TrimSpace(account.GetClaudeUserID())
	}
	if deviceID == "" && account != nil {
		deviceID = deterministicRewriteDeviceID(account.ID)
	}
	if deviceID == "" {
		deviceID = deterministicRewriteDeviceID(0)
	}

	platform := strings.TrimSpace(accountExtraString(account, "cc_rewrite_platform"))
	if platform == "" {
		platform = defaultRewritePlatform(fp)
	}

	shell := strings.TrimSpace(accountExtraString(account, "cc_rewrite_shell"))
	osVersion := strings.TrimSpace(accountExtraString(account, "cc_rewrite_os_version"))
	workingDir := strings.TrimSpace(accountExtraString(account, "cc_rewrite_working_dir"))
	if shell == "" || osVersion == "" || workingDir == "" {
		defaultShell, defaultOSVersion, defaultWorkingDir := defaultPromptEnvForPlatform(platform)
		if shell == "" {
			shell = defaultShell
		}
		if osVersion == "" {
			osVersion = defaultOSVersion
		}
		if workingDir == "" {
			workingDir = defaultWorkingDir
		}
	}

	userAgent := strings.TrimSpace(accountExtraString(account, "cc_rewrite_user_agent"))
	if userAgent == "" {
		userAgent = fmt.Sprintf("claude-cli/%s (external, cli)", version)
	}

	return ClaudeCodeRewriteProfile{
		DeviceID:   deviceID,
		Version:    version,
		UserAgent:  userAgent,
		Platform:   platform,
		Shell:      shell,
		OSVersion:  osVersion,
		WorkingDir: workingDir,
	}
}

func RewriteClaudeCodeRequestBody(body []byte, path string, profile ClaudeCodeRewriteProfile) ([]byte, bool, error) {
	if len(body) == 0 || !gjson.ValidBytes(body) {
		return body, false, nil
	}

	out := body
	modified := false

	if !strings.HasPrefix(path, "/v1/messages") {
		if next, changed := rewriteClaudeCodeLeakFields(out); changed {
			return next, true, nil
		}
		return out, false, nil
	}

	if next, changed := rewriteClaudeCodeMetadataUserID(out, profile.DeviceID); changed {
		out = next
		modified = true
	}

	if next, changed, err := rewriteClaudeCodeMessages(out, profile); err != nil {
		return body, false, err
	} else if changed {
		out = next
		modified = true
	}

	firstUserText := extractFirstUserMessageText(out)
	hash := ccRewriteFallbackHash
	if firstUserText != "" {
		hash = computeClaudeCodeRewriteHash(firstUserText, profile.Version)
	}

	if next, changed, err := rewriteClaudeCodeSystem(out, profile, hash); err != nil {
		return body, false, err
	} else if changed {
		out = next
		modified = true
	}

	if next, changed := rewriteClaudeCodeLeakFields(out); changed {
		out = next
		modified = true
	}

	return out, modified, nil
}

func ApplyClaudeCodeUpstreamHeaderRewrite(req *http.Request, profile ClaudeCodeRewriteProfile) {
	if req == nil {
		return
	}
	req.Header.Del("x-anthropic-billing-header")
	req.Header.Del("X-Anthropic-Billing-Header")
	if strings.TrimSpace(profile.UserAgent) != "" {
		req.Header.Set("User-Agent", profile.UserAgent)
	}
}

func rewriteClaudeCodeMetadataUserID(body []byte, deviceID string) ([]byte, bool) {
	if strings.TrimSpace(deviceID) == "" {
		return body, false
	}

	userID := gjson.GetBytes(body, "metadata.user_id")
	if !userID.Exists() || userID.Type != gjson.String {
		return body, false
	}

	raw := strings.TrimSpace(userID.String())
	if raw == "" || !strings.HasPrefix(raw, "{") {
		return body, false
	}

	var payload map[string]any
	if err := json.Unmarshal([]byte(raw), &payload); err != nil {
		return body, false
	}
	currentDeviceID, _ := payload["device_id"].(string)
	if strings.TrimSpace(currentDeviceID) == strings.TrimSpace(deviceID) {
		return body, false
	}
	payload["device_id"] = deviceID

	encoded, err := json.Marshal(payload)
	if err != nil {
		return body, false
	}
	next, ok := setJSONValueBytes(body, "metadata.user_id", string(encoded))
	return next, ok
}

func rewriteClaudeCodeMessages(body []byte, profile ClaudeCodeRewriteProfile) ([]byte, bool, error) {
	messagesResult := gjson.GetBytes(body, "messages")
	if !messagesResult.Exists() || !messagesResult.IsArray() {
		return body, false, nil
	}

	var messages []map[string]any
	if err := json.Unmarshal(sliceRawFromBody(body, messagesResult), &messages); err != nil {
		return body, false, err
	}

	changed := false
	for i := range messages {
		content := messages[i]["content"]
		switch v := content.(type) {
		case string:
			rewritten := rewriteClaudeCodeSystemReminders(v, profile)
			if rewritten != v {
				messages[i]["content"] = rewritten
				changed = true
			}
		case []any:
			for j, blockAny := range v {
				block, ok := blockAny.(map[string]any)
				if !ok {
					continue
				}
				text, ok := block["text"].(string)
				if !ok {
					continue
				}
				rewritten := rewriteClaudeCodeSystemReminders(text, profile)
				if rewritten == text {
					continue
				}
				block["text"] = rewritten
				v[j] = block
				changed = true
			}
			if changed {
				messages[i]["content"] = v
			}
		}
	}

	if !changed {
		return body, false, nil
	}

	raw, err := json.Marshal(messages)
	if err != nil {
		return body, false, err
	}
	next, ok := setJSONRawBytes(body, "messages", raw)
	return next, ok, nil
}

func rewriteClaudeCodeSystem(body []byte, profile ClaudeCodeRewriteProfile, hash string) ([]byte, bool, error) {
	systemResult := gjson.GetBytes(body, "system")
	if !systemResult.Exists() {
		return body, false, nil
	}

	switch {
	case systemResult.Type == gjson.String:
		original := systemResult.String()
		rewritten := ccRewriteBillingInlinePattern.ReplaceAllString(original, "")
		rewritten = rewriteClaudeCodePromptText(rewritten, profile, hash)
		if rewritten == original {
			return body, false, nil
		}
		next, ok := setJSONValueBytes(body, "system", rewritten)
		return next, ok, nil
	case systemResult.IsArray():
		var system []any
		if err := json.Unmarshal(sliceRawFromBody(body, systemResult), &system); err != nil {
			return body, false, err
		}

		filtered := make([]any, 0, len(system))
		changed := false
		for _, item := range system {
			switch v := item.(type) {
			case string:
				if ccRewriteBillingBlockPattern.MatchString(v) {
					changed = true
					continue
				}
				rewritten := rewriteClaudeCodePromptText(v, profile, hash)
				if rewritten != v {
					changed = true
					filtered = append(filtered, rewritten)
					continue
				}
				filtered = append(filtered, item)
			case map[string]any:
				text, _ := v["text"].(string)
				if ccRewriteBillingBlockPattern.MatchString(text) {
					changed = true
					continue
				}
				rewritten := rewriteClaudeCodePromptText(text, profile, hash)
				if rewritten != text {
					v["text"] = rewritten
					changed = true
				}
				filtered = append(filtered, v)
			default:
				filtered = append(filtered, item)
			}
		}

		if !changed {
			return body, false, nil
		}

		raw, err := json.Marshal(filtered)
		if err != nil {
			return body, false, err
		}
		next, ok := setJSONRawBytes(body, "system", raw)
		return next, ok, nil
	default:
		return body, false, nil
	}
}

func rewriteClaudeCodeLeakFields(body []byte) ([]byte, bool) {
	out := body
	changed := false
	for _, key := range []string{"baseUrl", "base_url", "gateway"} {
		if !gjson.GetBytes(out, key).Exists() {
			continue
		}
		if next, ok := deleteJSONPathBytes(out, key); ok {
			out = next
			changed = true
		}
	}
	return out, changed
}

func rewriteClaudeCodeSystemReminders(text string, profile ClaudeCodeRewriteProfile) string {
	if text == "" {
		return text
	}
	return ccRewriteSystemReminderRe.ReplaceAllStringFunc(text, func(match string) string {
		parts := ccRewriteSystemReminderRe.FindStringSubmatch(match)
		if len(parts) != 4 {
			return match
		}
		return parts[1] + rewriteClaudeCodePromptText(parts[2], profile, "") + parts[3]
	})
}

func rewriteClaudeCodePromptText(text string, profile ClaudeCodeRewriteProfile, hash string) string {
	if text == "" {
		return text
	}

	result := text
	if strings.TrimSpace(hash) != "" {
		result = ccRewriteCCVersionPattern.ReplaceAllString(result, fmt.Sprintf("cc_version=%s.%s", profile.Version, hash))
	}
	result = ccRewritePlatformPattern.ReplaceAllString(result, "Platform: "+profile.Platform)
	result = ccRewriteShellPattern.ReplaceAllString(result, "Shell: "+profile.Shell)
	result = ccRewriteOSVersionPattern.ReplaceAllString(result, "OS Version: "+profile.OSVersion)
	result = ccRewriteWorkingDirPattern.ReplaceAllString(result, "${1}"+profile.WorkingDir)
	result = ccRewriteHomePrefixPattern.ReplaceAllString(result, rewriteHomePrefix(profile.WorkingDir))
	return result
}

func computeClaudeCodeRewriteHash(firstUserMessageText string, version string) string {
	if firstUserMessageText == "" {
		return ccRewriteFallbackHash
	}
	chars := make([]byte, 0, len(ccRewriteHashPositions))
	for _, pos := range ccRewriteHashPositions {
		if pos >= 0 && pos < len(firstUserMessageText) {
			chars = append(chars, firstUserMessageText[pos])
			continue
		}
		chars = append(chars, '0')
	}
	seed := ccRewriteSalt + string(chars) + version
	sum := deterministicRewriteDeviceIDFromSeed(seed)
	return sum[:3]
}

func extractFirstUserMessageText(body []byte) string {
	messagesResult := gjson.GetBytes(body, "messages")
	if !messagesResult.Exists() || !messagesResult.IsArray() {
		return ""
	}
	for _, msg := range messagesResult.Array() {
		if msg.Get("role").String() != "user" {
			continue
		}
		content := msg.Get("content")
		switch {
		case content.Type == gjson.String:
			return content.String()
		case content.IsArray():
			for _, block := range content.Array() {
				if block.Get("type").String() != "text" {
					continue
				}
				if text := block.Get("text").String(); text != "" {
					return text
				}
			}
		}
	}
	return ""
}

func rewriteHomePrefix(workingDir string) string {
	workingDir = strings.TrimSpace(workingDir)
	switch {
	case strings.HasPrefix(workingDir, "/Users/"):
		parts := strings.Split(strings.TrimPrefix(workingDir, "/"), "/")
		if len(parts) >= 2 {
			return "/" + parts[0] + "/" + parts[1] + "/"
		}
	case strings.HasPrefix(workingDir, "/home/"):
		parts := strings.Split(strings.TrimPrefix(workingDir, "/"), "/")
		if len(parts) >= 2 {
			return "/" + parts[0] + "/" + parts[1] + "/"
		}
	}
	return "/Users/claude/"
}

func resolveRewriteUserAgent(fp *Fingerprint, headers http.Header) string {
	if fp != nil && strings.TrimSpace(fp.UserAgent) != "" {
		return strings.TrimSpace(fp.UserAgent)
	}
	if headers != nil {
		if ua := strings.TrimSpace(headers.Get("User-Agent")); ua != "" {
			return ua
		}
	}
	return defaultFingerprint.UserAgent
}

func defaultRewritePlatform(fp *Fingerprint) string {
	if fp != nil {
		switch strings.ToLower(strings.TrimSpace(fp.StainlessOS)) {
		case "linux":
			return "linux"
		case "darwin", "macos", "mac", "mac os":
			return "darwin"
		}
	}
	return "darwin"
}

func defaultPromptEnvForPlatform(platform string) (shell, osVersion, workingDir string) {
	switch strings.ToLower(strings.TrimSpace(platform)) {
	case "linux":
		return "bash", "Linux 6.5.0-generic", "/home/claude/projects"
	default:
		return "zsh", "Darwin 24.4.0", "/Users/claude/projects"
	}
}

func deterministicRewriteDeviceID(accountID int64) string {
	return deterministicRewriteDeviceIDFromSeed(fmt.Sprintf("claude-rewrite-account:%d", accountID))
}

func deterministicRewriteDeviceIDFromSeed(seed string) string {
	sumBytes := sha256.Sum256([]byte(seed))
	sum := fmt.Sprintf("%x", sumBytes[:])
	if len(sum) >= 64 {
		return sum[:64]
	}
	if len(sum) == 0 {
		return strings.Repeat("0", 64)
	}
	for len(sum) < 64 {
		sum += sum
	}
	return sum[:64]
}

func accountExtraString(account *Account, key string) string {
	if account == nil {
		return ""
	}
	return account.GetExtraString(key)
}
