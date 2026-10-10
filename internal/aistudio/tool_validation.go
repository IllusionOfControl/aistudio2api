package aistudio

import (
	"bytes"
	"encoding/json"
	"fmt"
)

// supportedTools discards function declarations and built-in tools unsupported by the model; clears tool choice if all declared tools are dropped, returning an error for unknown or incompatible built-in tools.
func supportedTools(tools Tools, model Model) (Tools, error) {
	if tools.ToolConfig.Mode == "none" {
		return tools, nil
	}
	declared := len(tools.Functions) > 0 || len(tools.Google) > 0 || tools.GoogleSearch != nil
	if !model.Capabilities["function_declarations"] && len(tools.Functions) > 0 {
		tools.Functions = nil
		tools.ToolConfig.AllowedFunctionNames = nil
	}
	if search := tools.GoogleSearch; search != nil {
		filtered := *search
		filtered.WebSearch = (search.WebSearch || !search.ImageSearch) && model.Capabilities["google_search"]
		filtered.ImageSearch = search.ImageSearch && model.Capabilities["image_search"]
		tools.GoogleSearch = nil
		if filtered.WebSearch || filtered.ImageSearch {
			tools.GoogleSearch = &filtered
		}
	}
	google := make([]string, 0, len(tools.Google))
	hasMaps := false
	hasCode := false
	hasURLContext := false
	for _, tool := range tools.Google {
		capability := ""
		switch tool {
		case "code_execution", "google_search", "image_search", "google_maps":
			capability = tool
		case "url_context":
			capability = "browse"
		default:
			return Tools{}, fmt.Errorf("unknown Google tool %q", tool)
		}
		if !model.Capabilities[capability] {
			continue
		}
		google = append(google, tool)
		hasMaps = hasMaps || tool == "google_maps"
		hasCode = hasCode || tool == "code_execution"
		hasURLContext = hasURLContext || tool == "url_context"
	}
	tools.Google = google
	if hasMaps && (hasCode || hasURLContext) {
		return Tools{}, fmt.Errorf("google_maps cannot be used together with code_execution or url_context")
	}
	if declared && len(tools.Functions) == 0 && len(tools.Google) == 0 && tools.GoogleSearch == nil {
		tools.ToolConfig = ToolConfig{}
	}
	return tools, nil
}

// transcribeFunctionParts rewrites function call and result parts to text transcripts, populating missing names
func transcribeFunctionParts(contents []Content) ([]Content, error) {
	transcribed := cloneContentsForFileCopies(contents)
	var pendingCalls []FunctionCall
	for contentIndex, content := range transcribed {
		linked, err := linkFunctionResults(content, &pendingCalls)
		if err != nil {
			return nil, fmt.Errorf("contents[%d]: %w", contentIndex, err)
		}
		for partIndex, part := range linked.Parts {
			if part.FunctionCall == nil && part.FunctionResult == nil {
				continue
			}
			transcript, err := toolPartTranscript(part)
			if err != nil {
				return nil, fmt.Errorf("contents[%d].parts[%d]: %w", contentIndex, partIndex, err)
			}
			content.Parts[partIndex] = Part{Text: transcript}
		}
	}
	return transcribed, nil
}

// toolPartTranscript returns Gemini API JSON text for function call, result, and code execution parts
func toolPartTranscript(part Part) (string, error) {
	var value map[string]any
	switch {
	case part.FunctionCall != nil:
		value = map[string]any{"functionCall": map[string]any{"name": part.FunctionCall.Name, "args": transcriptJSON(part.FunctionCall.Arguments)}}
	case part.FunctionResult != nil:
		value = map[string]any{"functionResponse": map[string]any{"name": part.FunctionResult.Name, "response": transcriptJSON(part.FunctionResult.Content)}}
	case part.ExecutableCode != nil:
		value = map[string]any{"executableCode": part.ExecutableCode}
	case part.CodeExecutionResult != nil:
		value = map[string]any{"codeExecutionResult": part.CodeExecutionResult}
	}
	encoded, err := json.Marshal(value)
	if err != nil {
		return "", fmt.Errorf("transcribing tool part: %w", err)
	}
	return string(encoded), nil
}

// transcriptJSON returns JSON value in transcript, empty recorded as empty object
func transcriptJSON(raw json.RawMessage) json.RawMessage {
	if len(bytes.TrimSpace(raw)) == 0 {
		return json.RawMessage(`{}`)
	}
	return raw
}
