package service

import (
	"encoding/json"
	"strings"

	"github.com/Wei-Shaw/sub2api/internal/pkg/apicompat"
)

func extractCodexFinalResponseObject(body string) (*apicompat.ResponsesResponse, bool) {
	var finalResp *apicompat.ResponsesResponse

	for _, line := range strings.Split(body, "\n") {
		data, ok := extractOpenAISSEDataLine(line)
		if !ok || data == "" || data == "[DONE]" {
			continue
		}

		var event apicompat.ResponsesStreamEvent
		if err := json.Unmarshal([]byte(data), &event); err != nil {
			continue
		}
		mergeBufferedResponsesEvent(&finalResp, &event)
	}

	if finalResp == nil {
		return nil, false
	}
	if finalResp.Object == "" {
		finalResp.Object = "response"
	}
	if finalResp.Output == nil {
		finalResp.Output = []apicompat.ResponsesOutput{}
	}
	return finalResp, true
}

func extractCodexFinalResponse(body string) ([]byte, bool) {
	finalResp, ok := extractCodexFinalResponseObject(body)
	if !ok {
		return nil, false
	}
	encoded, err := json.Marshal(finalResp)
	if err != nil {
		return nil, false
	}
	return encoded, true
}

func extractOpenAIUsageFromResponses(resp *apicompat.ResponsesResponse) (OpenAIUsage, bool) {
	if resp == nil || resp.Usage == nil {
		return OpenAIUsage{}, false
	}

	usage := OpenAIUsage{
		InputTokens:  resp.Usage.InputTokens,
		OutputTokens: resp.Usage.OutputTokens,
	}
	if resp.Usage.InputTokensDetails != nil {
		usage.CacheReadInputTokens = resp.Usage.InputTokensDetails.CachedTokens
	}
	return usage, true
}

func mergeBufferedResponsesEvent(dst **apicompat.ResponsesResponse, event *apicompat.ResponsesStreamEvent) {
	if event == nil {
		return
	}

	switch event.Type {
	case "response.created", "response.completed", "response.incomplete", "response.failed", "response.done":
		if event.Response != nil {
			mergeBufferedResponsesResponse(dst, event.Response)
		}
	case "response.output_item.added", "response.output_item.done":
		if event.Item == nil {
			return
		}
		resp := ensureBufferedResponsesResponse(dst)
		item := ensureBufferedResponsesOutput(resp, event.OutputIndex, event.Item.Type, event.ItemID)
		mergeBufferedResponsesOutput(item, event.Item)
	case "response.output_text.delta":
		resp := ensureBufferedResponsesResponse(dst)
		item := ensureBufferedResponsesOutput(resp, event.OutputIndex, "message", event.ItemID)
		if item.Role == "" {
			item.Role = "assistant"
		}
		part := ensureBufferedResponsesContentPart(item, event.ContentIndex)
		part.Type = "output_text"
		part.Text += event.Delta
	case "response.output_text.done":
		resp := ensureBufferedResponsesResponse(dst)
		item := ensureBufferedResponsesOutput(resp, event.OutputIndex, "message", event.ItemID)
		if item.Role == "" {
			item.Role = "assistant"
		}
		part := ensureBufferedResponsesContentPart(item, event.ContentIndex)
		part.Type = "output_text"
		part.Text = mergeBufferedFinalString(part.Text, event.Text)
	case "response.function_call_arguments.delta":
		resp := ensureBufferedResponsesResponse(dst)
		item := ensureBufferedResponsesOutput(resp, event.OutputIndex, "function_call", event.ItemID)
		if event.CallID != "" {
			item.CallID = event.CallID
		}
		if event.Name != "" {
			item.Name = event.Name
		}
		item.Arguments += event.Delta
	case "response.function_call_arguments.done":
		resp := ensureBufferedResponsesResponse(dst)
		item := ensureBufferedResponsesOutput(resp, event.OutputIndex, "function_call", event.ItemID)
		if event.CallID != "" {
			item.CallID = event.CallID
		}
		if event.Name != "" {
			item.Name = event.Name
		}
		item.Arguments = mergeBufferedFinalString(item.Arguments, event.Arguments)
	case "response.reasoning_summary_text.delta":
		resp := ensureBufferedResponsesResponse(dst)
		item := ensureBufferedResponsesOutput(resp, event.OutputIndex, "reasoning", event.ItemID)
		summary := ensureBufferedResponsesSummary(item, event.SummaryIndex)
		summary.Type = "summary_text"
		summary.Text += event.Delta
	case "response.reasoning_summary_text.done":
		resp := ensureBufferedResponsesResponse(dst)
		item := ensureBufferedResponsesOutput(resp, event.OutputIndex, "reasoning", event.ItemID)
		summary := ensureBufferedResponsesSummary(item, event.SummaryIndex)
		summary.Type = "summary_text"
		summary.Text = mergeBufferedFinalString(summary.Text, event.Text)
	}
}

func ensureBufferedResponsesResponse(dst **apicompat.ResponsesResponse) *apicompat.ResponsesResponse {
	if *dst == nil {
		*dst = &apicompat.ResponsesResponse{
			Object: "response",
			Output: []apicompat.ResponsesOutput{},
		}
	}
	if (*dst).Output == nil {
		(*dst).Output = []apicompat.ResponsesOutput{}
	}
	return *dst
}

func mergeBufferedResponsesResponse(dst **apicompat.ResponsesResponse, src *apicompat.ResponsesResponse) {
	if src == nil {
		return
	}
	resp := ensureBufferedResponsesResponse(dst)

	if src.ID != "" {
		resp.ID = src.ID
	}
	if src.Object != "" {
		resp.Object = src.Object
	}
	if src.Model != "" {
		resp.Model = src.Model
	}
	if src.Status != "" {
		resp.Status = src.Status
	}
	if src.IncompleteDetails != nil {
		details := *src.IncompleteDetails
		resp.IncompleteDetails = &details
	}
	if src.Error != nil {
		errCopy := *src.Error
		resp.Error = &errCopy
	}
	if src.Usage != nil {
		usageCopy := *src.Usage
		if src.Usage.InputTokensDetails != nil {
			detailsCopy := *src.Usage.InputTokensDetails
			usageCopy.InputTokensDetails = &detailsCopy
		}
		if src.Usage.OutputTokensDetails != nil {
			detailsCopy := *src.Usage.OutputTokensDetails
			usageCopy.OutputTokensDetails = &detailsCopy
		}
		resp.Usage = &usageCopy
	}
	for idx := range src.Output {
		item := ensureBufferedResponsesOutput(resp, idx, src.Output[idx].Type, src.Output[idx].ID)
		mergeBufferedResponsesOutput(item, &src.Output[idx])
	}
}

func ensureBufferedResponsesOutput(resp *apicompat.ResponsesResponse, index int, itemType, itemID string) *apicompat.ResponsesOutput {
	if index < 0 {
		index = 0
	}
	for len(resp.Output) <= index {
		resp.Output = append(resp.Output, apicompat.ResponsesOutput{})
	}
	item := &resp.Output[index]
	if item.Type == "" && itemType != "" {
		item.Type = itemType
	}
	if item.ID == "" && itemID != "" {
		item.ID = itemID
	}
	return item
}

func mergeBufferedResponsesOutput(dst *apicompat.ResponsesOutput, src *apicompat.ResponsesOutput) {
	if dst == nil || src == nil {
		return
	}
	if src.Type != "" {
		dst.Type = src.Type
	}
	if src.ID != "" {
		dst.ID = src.ID
	}
	if src.Role != "" {
		dst.Role = src.Role
	}
	if src.Status != "" {
		dst.Status = src.Status
	}
	if src.EncryptedContent != "" {
		dst.EncryptedContent = src.EncryptedContent
	}
	if src.CallID != "" {
		dst.CallID = src.CallID
	}
	if src.Name != "" {
		dst.Name = src.Name
	}
	if src.Arguments != "" {
		dst.Arguments = mergeBufferedFinalString(dst.Arguments, src.Arguments)
	}
	if src.Action != nil {
		actionCopy := *src.Action
		dst.Action = &actionCopy
	}
	for idx := range src.Content {
		part := ensureBufferedResponsesContentPart(dst, idx)
		if src.Content[idx].Type != "" {
			part.Type = src.Content[idx].Type
		}
		if src.Content[idx].Text != "" {
			part.Text = mergeBufferedFinalString(part.Text, src.Content[idx].Text)
		}
		if src.Content[idx].ImageURL != "" {
			part.ImageURL = src.Content[idx].ImageURL
		}
	}
	for idx := range src.Summary {
		summary := ensureBufferedResponsesSummary(dst, idx)
		if src.Summary[idx].Type != "" {
			summary.Type = src.Summary[idx].Type
		}
		if src.Summary[idx].Text != "" {
			summary.Text = mergeBufferedFinalString(summary.Text, src.Summary[idx].Text)
		}
	}
}

func ensureBufferedResponsesContentPart(item *apicompat.ResponsesOutput, index int) *apicompat.ResponsesContentPart {
	if index < 0 {
		index = 0
	}
	for len(item.Content) <= index {
		item.Content = append(item.Content, apicompat.ResponsesContentPart{})
	}
	return &item.Content[index]
}

func ensureBufferedResponsesSummary(item *apicompat.ResponsesOutput, index int) *apicompat.ResponsesSummary {
	if index < 0 {
		index = 0
	}
	for len(item.Summary) <= index {
		item.Summary = append(item.Summary, apicompat.ResponsesSummary{})
	}
	return &item.Summary[index]
}

func mergeBufferedFinalString(current, final string) string {
	switch {
	case final == "":
		return current
	case current == "":
		return final
	case strings.HasPrefix(final, current):
		return final
	default:
		return current
	}
}
