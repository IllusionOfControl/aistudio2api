package api

import (
	"encoding/json"
	"fmt"
	"mime"
	"strings"

	"github.com/Mag1cFall/AIStudio2API/internal/aistudio"
)

// interactionRequest represents a public Interactions creation request.
type interactionRequest struct {
	Model      string          `json:"model"`
	Input      json.RawMessage `json:"input"`
	System     string          `json:"system_instruction"`
	Stream     bool            `json:"stream"`
	Store      *bool           `json:"store"`
	PreviousID string          `json:"previous_interaction_id"`
	Formats    json.RawMessage `json:"response_format"`
	Tools      []responsesTool `json:"tools"`
	Generation struct {
		Temperature       *float64        `json:"temperature"`
		TopP              *float64        `json:"top_p"`
		TopK              *int            `json:"top_k"`
		MaxOutputTokens   *int64          `json:"max_output_tokens"`
		Seed              *int64          `json:"seed"`
		StopSequences     []string        `json:"stop_sequences"`
		ThinkingLevel     string          `json:"thinking_level"`
		ThinkingSummaries string          `json:"thinking_summaries"`
		Speech            json.RawMessage `json:"speech_config"`
		ToolChoice        json.RawMessage `json:"tool_choice"`
	} `json:"generation_config"`
}

// interactionFormat specifies return configurations for text, image, and audio.
type interactionFormat struct {
	Type        string          `json:"type"`
	MIME        string          `json:"mime_type"`
	Schema      json.RawMessage `json:"schema"`
	SampleRate  int             `json:"sample_rate"`
	BitRate     int             `json:"bit_rate"`
	AspectRatio string          `json:"aspect_ratio"`
	ImageSize   string          `json:"image_size"`
}

// interactionInput represents an input content chunk or execution step.
type interactionInput struct {
	Type        string             `json:"type"`
	Text        *string            `json:"text"`
	Data        string             `json:"data"`
	URI         string             `json:"uri"`
	MIME        string             `json:"mime_type"`
	Content     []interactionInput `json:"content"`
	Summary     []interactionInput `json:"summary"`
	ID          string             `json:"id"`
	CallID      string             `json:"call_id"`
	Name        string             `json:"name"`
	Arguments   json.RawMessage    `json:"arguments"`
	Result      json.RawMessage    `json:"result"`
	IsError     bool               `json:"is_error"`
	Signature   string             `json:"signature"`
	Annotations []struct {
		Type    string `json:"type"`
		Speaker string `json:"speaker"`
		Style   string `json:"style"`
	} `json:"annotations"`
}

// interactionList parses single-object or array union fields in the protocol.
func interactionList[T any](raw json.RawMessage) ([]T, error) {
	value := strings.TrimSpace(string(raw))
	if value == "" || value == "null" {
		return nil, nil
	}
	var values []T
	if strings.HasPrefix(value, "[") {
		err := json.Unmarshal(raw, &values)
		return values, err
	}
	var item T
	if err := json.Unmarshal(raw, &item); err != nil {
		return nil, err
	}
	return []T{item}, nil
}

// toGenerateRequest maps model, structured input, generation parameters, and function declarations.
func (request interactionRequest) toGenerateRequest(id string) (aistudio.GenerateRequest, error) {
	generate := aistudio.GenerateRequest{ID: id, Model: strings.TrimPrefix(strings.TrimSpace(request.Model), "models/"), System: request.System}
	if generate.Model == "" {
		return generate, fmt.Errorf("model is required")
	}
	var err error
	generate.Contents, err = interactionContents(request.Input)
	if err != nil {
		return generate, err
	}
	generation := request.Generation
	generate.Config = aistudio.GenerationConfig{
		Temperature: generation.Temperature, TopP: generation.TopP, TopK: generation.TopK,
		MaxOutputTokens: generation.MaxOutputTokens, Seed: generation.Seed,
		StopSequences: normalizeStopSequences(generation.StopSequences), ReasoningEffort: generation.ThinkingLevel,
	}
	if generation.ThinkingSummaries != "" && generation.ThinkingSummaries != "auto" && generation.ThinkingSummaries != "none" {
		return generate, fmt.Errorf("thinking_summaries must be auto or none")
	}
	generate.Config.SpeechConfig, err = interactionSpeech(generation.Speech)
	if err != nil {
		return generate, err
	}
	formats, err := interactionList[json.RawMessage](request.Formats)
	if err != nil {
		return generate, fmt.Errorf("response_format: %w", err)
	}
	for _, raw := range formats {
		var format interactionFormat
		if err := json.Unmarshal(raw, &format); err != nil {
			return generate, fmt.Errorf("response_format: %w", err)
		}
		switch format.Type {
		case "text":
			generate.Config.ResponseModalities = append(generate.Config.ResponseModalities, aistudio.ResponseModalityText)
			if format.MIME == "" && len(format.Schema) > 0 {
				format.MIME = "application/json"
			}
			if format.MIME != "" && format.MIME != "text/plain" && format.MIME != "application/json" {
				return generate, fmt.Errorf("text mime_type must be text/plain or application/json")
			}
			generate.Config.ResponseMIMEType = format.MIME
			generate.Config.ResponseSchema = format.Schema
			if len(format.Schema) > 0 && format.MIME != "application/json" {
				return generate, fmt.Errorf("response_format.schema requires application/json")
			}
		case "", "object", "array", "string", "number", "integer", "boolean", "null":
			generate.Config.ResponseModalities = append(generate.Config.ResponseModalities, aistudio.ResponseModalityText)
			generate.Config.ResponseMIMEType = "application/json"
			generate.Config.ResponseSchema = raw
		case "audio":
			if _, ok := interactionAudioEncodings[format.MIME]; format.MIME != "" && !ok {
				return generate, fmt.Errorf("audio mime_type must be audio/wav, audio/l16, audio/mp3, audio/ogg_opus, audio/alaw or audio/mulaw")
			}
			if format.SampleRate < 0 || format.BitRate < 0 {
				return generate, fmt.Errorf("audio sample_rate and bit_rate must not be negative")
			}
			generate.Config.ResponseModalities = append(generate.Config.ResponseModalities, aistudio.ResponseModalityAudio)
		case "image":
			if format.MIME != "" && format.MIME != "image/jpeg" {
				return generate, fmt.Errorf("image mime_type must be image/jpeg")
			}
			generate.Config.ResponseModalities = append(generate.Config.ResponseModalities, aistudio.ResponseModalityImage)
			generate.Config.ImageConfig = &aistudio.ImageConfig{AspectRatio: format.AspectRatio, ImageSize: format.ImageSize}
		default:
			return generate, fmt.Errorf("unsupported response_format type %q", format.Type)
		}
	}
	tools := make([]responsesTool, 0, len(request.Tools))
	for _, tool := range request.Tools {
		switch tool.Type {
		case "function", "url_context", "google_maps":
		case "google_search":
			tool.Type = "web_search"
		case "code_execution":
			tool.Type = "code_interpreter"
		case "computer_use", "file_search", "mcp_server", "retrieval":
			continue
		default:
			return generate, fmt.Errorf("unsupported interaction tool %q", tool.Type)
		}
		tools = append(tools, tool)
	}
	generate.Tools, err = mapResponsesTools(tools, nil)
	if err != nil {
		return generate, err
	}
	generate.Tools.ToolConfig, err = interactionToolChoice(generation.ToolChoice)
	return generate, err
}

// interactionToolChoice maps call mode or allowed_tools configuration to unified tool choice.
func interactionToolChoice(raw json.RawMessage) (aistudio.ToolConfig, error) {
	if !rawJSONConfigured(raw) {
		return aistudio.ToolConfig{Mode: "auto"}, nil
	}
	var mode string
	var names []string
	if json.Unmarshal(raw, &mode) != nil {
		var config struct {
			AllowedTools struct {
				Mode  string   `json:"mode"`
				Tools []string `json:"tools"`
			} `json:"allowed_tools"`
		}
		if err := json.Unmarshal(raw, &config); err != nil {
			return aistudio.ToolConfig{}, fmt.Errorf("generation_config.tool_choice: %w", err)
		}
		mode, names = config.AllowedTools.Mode, config.AllowedTools.Tools
	}
	switch mode {
	case "", "auto":
		mode = "auto"
	case "any":
		mode = "required"
	case "none", "validated":
	default:
		return aistudio.ToolConfig{}, fmt.Errorf("unsupported generation_config.tool_choice %q", mode)
	}
	return aistudio.ToolConfig{Mode: mode, AllowedFunctionNames: names}, nil
}

// interactionContents preserves step order and merges content chunks of the same role.
func interactionContents(raw json.RawMessage) ([]aistudio.Content, error) {
	var text string
	if json.Unmarshal(raw, &text) == nil {
		if strings.TrimSpace(text) == "" {
			return nil, nil
		}
		return []aistudio.Content{{Role: aistudio.RoleUser, Parts: []aistudio.Part{{Text: text}}}}, nil
	}
	items, err := interactionList[interactionInput](raw)
	if err != nil {
		return nil, fmt.Errorf("input: %w", err)
	}
	var contents []aistudio.Content
	pendingSignature := ""
	for _, item := range items {
		role := aistudio.RoleUser
		var parts []aistudio.Part
		switch item.Type {
		case "user_input", "model_output":
			if item.Type == "model_output" {
				role = aistudio.RoleAssistant
			}
			for _, content := range item.Content {
				mapped, mapErr := content.parts()
				if mapErr != nil {
					return nil, mapErr
				}
				parts = append(parts, mapped...)
			}
		case "function_call":
			if item.ID == "" || item.Name == "" || len(item.Arguments) == 0 {
				return nil, fmt.Errorf("function_call requires id, name and arguments")
			}
			role = aistudio.RoleAssistant
			parts = []aistudio.Part{{FunctionCall: &aistudio.FunctionCall{ID: item.ID, Name: item.Name, Arguments: item.Arguments}, ThoughtSignature: pendingSignature}}
			pendingSignature = ""
		case "function_result":
			if item.CallID == "" || len(item.Result) == 0 {
				return nil, fmt.Errorf("function_result requires call_id and result")
			}
			result, mapErr := normalizeFunctionResultContent(item.Result)
			if mapErr != nil {
				return nil, mapErr
			}
			role = aistudio.RoleTool
			parts = []aistudio.Part{{FunctionResult: &aistudio.FunctionResult{ID: item.CallID, Name: item.Name, Content: result}}}
		case "thought":
			role = aistudio.RoleAssistant
			if item.Signature != "" {
				pendingSignature = item.Signature
			}
		case "code_execution_call", "code_execution_result":
			role = aistudio.RoleAssistant
			if text := item.codeExecutionText(); text != "" {
				parts = []aistudio.Part{{Text: text}}
			}
		case "google_search_call", "google_search_result", "url_context_call", "url_context_result",
			"google_maps_call", "google_maps_result", "file_search_call", "file_search_result",
			"mcp_server_tool_call", "mcp_server_tool_result", "retrieval_call", "retrieval_result",
			"processing_call", "processing_result":
			continue
		default:
			parts, err = item.parts()
			if err != nil {
				return nil, err
			}
		}
		if len(parts) == 0 {
			continue
		}
		if pendingSignature != "" {
			if role == aistudio.RoleAssistant {
				parts[0].ThoughtSignature = pendingSignature
			} else if len(contents) > 0 && contents[len(contents)-1].Role == aistudio.RoleAssistant {
				contents[len(contents)-1].Parts = append(contents[len(contents)-1].Parts, aistudio.Part{ThoughtSignature: pendingSignature})
			}
			pendingSignature = ""
		}
		if len(contents) > 0 && contents[len(contents)-1].Role == role {
			contents[len(contents)-1].Parts = append(contents[len(contents)-1].Parts, parts...)
		} else {
			contents = append(contents, aistudio.Content{Role: role, Parts: parts})
		}
	}
	if pendingSignature != "" && len(contents) > 0 && contents[len(contents)-1].Role == aistudio.RoleAssistant {
		contents[len(contents)-1].Parts = append(contents[len(contents)-1].Parts, aistudio.Part{ThoughtSignature: pendingSignature})
	}
	return contents, nil
}

// codeExecutionText renders code and execution results from history as text matching service output format.
func (item interactionInput) codeExecutionText() string {
	if item.Type == "code_execution_call" {
		var call aistudio.ExecutableCode
		if json.Unmarshal(item.Arguments, &call) != nil {
			return ""
		}
		return renderCodeExecution(aistudio.Event{Kind: aistudio.EventExecutableCode, ExecutableCode: &call})
	}
	var output string
	if json.Unmarshal(item.Result, &output) != nil {
		return ""
	}
	result := aistudio.CodeExecutionResult{Outcome: "OUTCOME_OK", Output: output}
	if item.IsError {
		result = aistudio.CodeExecutionResult{Outcome: "OUTCOME_FAILED", Error: output}
	}
	return renderCodeExecution(aistudio.Event{Kind: aistudio.EventCodeExecutionResult, CodeExecutionResult: &result})
}

// parts reuses Gemini text, media, and file input mappings.
func (content interactionInput) parts() ([]aistudio.Part, error) {
	var part geminiPart
	switch content.Type {
	case "text":
		if content.Text == nil {
			return nil, fmt.Errorf("text content requires text")
		}
		part.Text = content.Text
		for _, annotation := range content.Annotations {
			if annotation.Type == "speech_metadata" {
				part.SpeechMetadata = &aistudio.SpeechMetadata{Speaker: annotation.Speaker, Style: annotation.Style}
			}
		}
	case "image", "audio", "video", "document":
		if (content.Data == "") == (content.URI == "") {
			return nil, fmt.Errorf("%s content requires exactly one of data or uri", content.Type)
		}
		if content.Data != "" {
			mimeType := content.MIME
			if mimeType == "" {
				data, err := decodeBase64Flexible(content.Data)
				if err != nil {
					return nil, fmt.Errorf("%s data: %w", content.Type, err)
				}
				mimeType = detectMediaType("", data)
				if mimeType == "application/octet-stream" {
					return nil, fmt.Errorf("%s content requires mime_type", content.Type)
				}
			}
			part.InlineData = &geminiBlobPart{MIMEType: mimeType, Data: content.Data}
		} else {
			part.FileData = &geminiFilePart{MIMEType: content.MIME, FileURI: content.URI}
		}
	default:
		return nil, fmt.Errorf("unsupported input type %q", content.Type)
	}
	parts, _, err := mapGeminiParts([]geminiPart{part})
	return parts, err
}

// interactionSpeech converts single voice list and multi-speaker configuration.
func interactionSpeech(raw json.RawMessage) (*aistudio.SpeechConfig, error) {
	if !rawJSONConfigured(raw) {
		return nil, nil
	}
	type voice struct {
		Voice   string `json:"voice"`
		Speaker string `json:"speaker"`
	}
	var config struct {
		Mode     string  `json:"mode"`
		Speakers []voice `json:"speakers"`
	}
	if strings.HasPrefix(strings.TrimSpace(string(raw)), "[") {
		if err := json.Unmarshal(raw, &config.Speakers); err != nil {
			return nil, err
		}
	} else if err := json.Unmarshal(raw, &config); err != nil {
		return nil, err
	}
	if len(config.Speakers) == 0 || len(config.Speakers) > 2 {
		return nil, fmt.Errorf("speech_config requires one or two voices")
	}
	speech := &aistudio.SpeechConfig{Mode: strings.ToUpper(config.Mode)}
	for _, speaker := range config.Speakers {
		if strings.TrimSpace(speaker.Voice) == "" {
			return nil, fmt.Errorf("speech_config voice is required")
		}
		if len(config.Speakers) == 1 && speaker.Speaker == "" {
			speech.VoiceName = speaker.Voice
		} else {
			if strings.TrimSpace(speaker.Speaker) == "" {
				return nil, fmt.Errorf("speech_config speaker is required for named voices")
			}
			speech.Speakers = append(speech.Speakers, aistudio.SpeakerVoiceConfig{Speaker: speaker.Speaker, VoiceName: speaker.Voice})
		}
	}
	return speech, nil
}

// imageMIME returns the MIME type requested by the image response format.
func (request interactionRequest) imageMIME() string {
	formats, _ := interactionList[interactionFormat](request.Formats)
	for _, format := range formats {
		if format.Type == "image" {
			return format.MIME
		}
	}
	return ""
}

// interactionAudioFormat represents the MIME, sample rate, and bit rate for Interactions audio output.
type interactionAudioFormat struct {
	MIME       string
	SampleRate int
	BitRate    int
}

// interactionAudioEncodings maps audio output MIME types to encoding format and content MIME.
var interactionAudioEncodings = map[string]struct{ format, content string }{
	"audio/wav": {"pcm", "audio/l16"}, "audio/l16": {"pcm", "audio/l16"}, "audio/mp3": {"mp3", "audio/mp3"},
	"audio/ogg_opus": {"opus", "audio/ogg"}, "audio/alaw": {"alaw", "audio/alaw"}, "audio/mulaw": {"mulaw", "audio/mulaw"},
}

// audioFormat returns audio output configuration; defaults to WAV for non-streaming and L16 for streaming when unspecified.
func (request interactionRequest) audioFormat() interactionAudioFormat {
	audio := interactionAudioFormat{MIME: "audio/wav"}
	if request.Stream {
		audio.MIME = "audio/l16"
	}
	formats, _ := interactionList[interactionFormat](request.Formats)
	for _, format := range formats {
		if format.Type == "audio" {
			audio.SampleRate, audio.BitRate = format.SampleRate, format.BitRate
			if format.MIME != "" {
				audio.MIME = format.MIME
			}
		}
	}
	return audio
}

// container returns packaging for interactionMedia: WAV packages PCM, other formats output raw encoded result
func (audio interactionAudioFormat) container() string {
	if audio.MIME == "audio/wav" {
		return "wav"
	}
	return "pcm"
}

// encode transcodes upstream PCM or MP3 to requested encoding and sample rate, preserving raw data when matching
func (audio interactionAudioFormat) encode(media aistudio.Media) (aistudio.Media, error) {
	baseType, _, _ := mime.ParseMediaType(media.MIME)
	if media.URL != "" || baseType != "audio/l16" && baseType != "audio/wav" && baseType != "audio/x-wav" && baseType != "audio/mpeg" {
		return media, nil
	}
	source, err := mediaPCM(media)
	if err != nil {
		return media, err
	}
	encoding := interactionAudioEncodings[audio.MIME]
	sameRate := audio.SampleRate == 0 || audio.SampleRate == source.SampleRate
	switch {
	case baseType == "audio/mpeg" && encoding.format == "mp3" && sameRate && audio.BitRate == 0:
		return aistudio.Media{MIME: fmt.Sprintf("%s;rate=%d;channels=%d", encoding.content, source.SampleRate, source.Channels), Data: media.Data}, nil
	case baseType != "audio/mpeg" && encoding.format == "pcm" && sameRate:
		return media, nil
	}
	data, encoded, err := encodeAudio(source, audioOutput{Format: encoding.format, SampleRate: audio.SampleRate, BitRate: audio.BitRate})
	return aistudio.Media{MIME: fmt.Sprintf("%s;rate=%d;channels=%d", encoding.content, encoded.SampleRate, encoded.Channels), Data: data}, err
}

// streams determines whether audio chunks can stream incrementally; container, resampling, and MP3 decoding require full audio
func (audio interactionAudioFormat) streams(media aistudio.Media) bool {
	switch audio.MIME {
	case "audio/l16", "audio/alaw", "audio/mulaw":
		if baseType, _, _ := mime.ParseMediaType(media.MIME); baseType == "audio/mpeg" {
			return false
		}
		source, err := mediaPCM(media)
		return err != nil || audio.SampleRate == 0 || audio.SampleRate == source.SampleRate
	}
	return false
}
