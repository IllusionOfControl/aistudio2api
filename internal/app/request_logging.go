package app

import (
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/Mag1cFall/AIStudio2API/internal/api"
	"github.com/Mag1cFall/AIStudio2API/internal/requestdb"
)

// requestLogData projects request identity, parameters, and end-to-end usage metrics.
func requestLogData(entry api.AccessLog) *api.RequestLog {
	data := &api.RequestLog{
		ID: entry.RequestID, Model: entry.Model, Method: entry.Method, Path: entry.Path,
		Status: entry.Status, DurationMS: float64(entry.Latency) / float64(time.Millisecond),
		ToolCalls: entry.ToolCalls, FinishReason: entry.FinishReason, Error: entry.Error,
		InputMessages: entry.InputMessages, InputTextChars: entry.InputTextChars,
		InputMedia: entry.InputMedia, InputMediaBytes: entry.InputMediaBytes, InputFiles: entry.InputFiles,
		FirstEventMS: float64(entry.FirstEvent) / float64(time.Millisecond), UpstreamBytes: entry.UpstreamBytes,
		QueueMS: float64(entry.QueueWait) / float64(time.Millisecond), ProofMS: float64(entry.Proof) / float64(time.Millisecond),
		Channel: entry.Channel,
	}

	if entry.Generation {
		data.Parameters = map[string]string{
			"temperature":       entry.Temperature,
			"top_p":             entry.TopP,
			"thinking":          strings.ToLower(entry.Thinking),
			"max_output_tokens": entry.MaxOutputTokens,
		}
	}

	if usage := entry.Usage; usage != nil {
		output := usage.OutputTokens + usage.ReasoningTokens
		data.Usage = &api.RequestLogUsage{
			InputTokens:     usage.InputTokens + usage.ToolTokens,
			ReasoningTokens: usage.ReasoningTokens,
			ReplyTokens:     usage.OutputTokens,
			OutputTokens:    output,
			TotalTokens:     usage.TotalTokens,
		}

		if entry.Latency > 0 {
			data.Usage.AverageTokensPerSecond = float64(output) / entry.Latency.Seconds()
		}
	}

	return data
}

// RecordAccessStart records the start of a request that can be correlated with subsequent events.
func (admin *runtimeAdmin) RecordAccessStart(entry api.AccessLog) {
	data := requestLogData(entry)
	data.State = "running"

	admin.recordRequestLog(entry.Account, "INFO", "request.started", "Request started", data)
}

// requestOutcome determines the request outcome state, log level, and summary message.
func requestOutcome(entry api.AccessLog) (string, string, string) {
	switch {
	case entry.Canceled || entry.Status == 499:
		return "cancelled", "WARN", "Request canceled"
	case entry.Status >= http.StatusBadRequest || entry.Error != "":
		return "failed", "ERROR", "Request failed"
	case entry.FinishReason == "max_tokens" || entry.FinishReason == "max_output_tokens" || entry.FinishReason == "length":
		return "limited", "WARN", "Output token limit reached"
	case entry.FinishReason != "" && entry.FinishReason != "stop" && entry.FinishReason != "stop_sequence":
		return "blocked", "WARN", "Upstream terminated generation"
	case entry.ToolCalls > 0:
		return "tool_calls", "INFO", "Tool calls completed"
	}
	return "completed", "INFO", "Request completed"
}

// finishedRequestLog projects request outcome log data, recording HTTP status when failed without an error message.
func finishedRequestLog(entry api.AccessLog) (*api.RequestLog, string, string) {
	data := requestLogData(entry)
	state, level, message := requestOutcome(entry)
	data.State = state
	if state == "failed" && data.Error == "" {
		data.Error = fmt.Sprintf("HTTP %d", entry.Status)
	}
	return data, level, message
}

// RecordAccessLog records request outcome log data.
func (admin *runtimeAdmin) RecordAccessLog(entry api.AccessLog) {
	data, level, message := finishedRequestLog(entry)
	admin.recordRequestLog(entry.Account, level, "request.finished", message, data)
}

// requestRow projects a POST request outcome into a usage ledger record.
func requestRow(entry api.AccessLog, finished time.Time) requestdb.Row {
	data, _, _ := finishedRequestLog(entry)
	row := requestdb.Row{
		ID: entry.RequestID, Time: finished, Protocol: requestProtocol(entry.Path), Path: entry.Path,
		Model: entry.Model, Account: entry.Account, Channel: entry.Channel, Status: entry.Status, State: data.State,
		Duration: entry.Latency, FirstEvent: entry.FirstEvent, Queue: entry.QueueWait, ToolCalls: entry.ToolCalls, Error: data.Error,
		Attempts: entry.Attempts,
	}
	if usage := data.Usage; usage != nil {
		row.InputTokens, row.ReasoningTokens, row.ReplyTokens, row.TotalTokens =
			usage.InputTokens, usage.ReasoningTokens, usage.ReplyTokens, usage.TotalTokens
	}
	return row
}

// requestProtocol categorizes request protocols by public API path.
func requestProtocol(path string) string {
	switch {
	case path == "/v1/chat/completions":
		return "openai-chat"
	case strings.HasPrefix(path, "/v1/responses"):
		return "openai-responses"
	case strings.HasPrefix(path, "/v1/messages"):
		return "anthropic"
	case strings.Contains(path, "/interactions"):
		return "interactions"
	case strings.Contains(path, "/files"):
		return "files"
	case strings.HasPrefix(path, "/v1beta/"):
		return "gemini"
	case strings.HasPrefix(path, "/v1/images/"):
		return "images"
	case strings.HasPrefix(path, "/v1/audio/"):
		return "audio"
	case strings.HasPrefix(path, "/v1/videos"):
		return "videos"
	}
	return "other"
}

// recordRequestLog standardizes the account source and event payload for request logs.
func (admin *runtimeAdmin) recordRequestLog(source, level, event, message string, data *api.RequestLog) {
	if source == "" {
		source = "request"
	}

	admin.requests.recordLog(api.AdminLog{
		Source:  source,
		Level:   level,
		Event:   event,
		Message: message,
		Request: data,
	})
}

// logRequestProgress associates waiting and recovery events with the corresponding request.
func (registry *requestRegistry) logRequestProgress(id, source, level, message string) {
	registry.recordLog(api.AdminLog{
		Source:  source,
		Level:   level,
		Event:   "request.progress",
		Message: message,
		Request: &api.RequestLog{
			ID:    id,
			State: "running",
		},
	})
}
