package aistudio

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"slices"
	"strings"
)

const (
	// buildBidiWebChannelURL is the WebChannel URL for Gemini Live WebSocket forwarded by the Build host page
	buildBidiWebChannelURL = "https://webchannel-alkalimakersuite-pa.clients6.google.com/v1/proxy:proxyBidiStreamedCall"
	// buildLivePath is the WebSocket path for Build proxy BidiGenerateContent
	buildLivePath = "/ws/google.ai.generativelanguage.v1beta.GenerativeService.BidiGenerateContent"
	// buildMusicPath is the WebSocket path for Build proxy BidiGenerateMusic
	buildMusicPath = "/ws/google.ai.generativelanguage.v1alpha.GenerativeService.BidiGenerateMusic"
)

// WeightedPrompt represents a weighted prompt for realtime music
type WeightedPrompt struct {
	Text   string  `json:"text"`
	Weight float64 `json:"weight"`
}

// buildPlaybackControls lists available playback controls for realtime music
var buildPlaybackControls = []string{"PLAY", "PAUSE", "STOP", "RESET_CONTEXT"}

// buildBidiPath returns the WebSocket path for the Build proxy based on the realtime method
func buildBidiPath(method string) string {
	if method == "bidiGenerateMusic" {
		return buildMusicPath
	}
	return buildLivePath
}

// encodeBuildBidiMessage encodes a Gemini Live JSON message into [path, JSON, proof] proxy envelope and returns binding
func encodeBuildBidiMessage(path string, message any) ([]byte, string, error) {
	body, err := json.Marshal(message)
	if err != nil {
		return nil, "", fmt.Errorf("encode Build realtime message: %w", err)
	}
	wire, err := EncodeBuildProxyRequest(path, body, false)
	if err != nil {
		return nil, "", fmt.Errorf("encode Build realtime proxy envelope: %w", err)
	}
	return wire, path + " " + string(body), nil
}

// EncodeBuildBidiSetup encodes Live or realtime music setup for Build proxy; Live fields match Playground setup semantics
func EncodeBuildBidiSetup(request BidiRequest) ([]byte, string, error) {
	model := strings.TrimPrefix(strings.TrimSpace(request.Model), "models/")
	setup := map[string]any{"model": wireModelName(model)}
	if request.method == "bidiGenerateMusic" {
		if len(request.Tools) > 0 || request.Translation != nil || request.Transcription != nil {
			return nil, "", fmt.Errorf("%w: realtime music model %s does not support tools, translation, or transcription", ErrInvalidArgument, model)
		}
		if output := strings.ToLower(strings.TrimSpace(request.OutputModality)); output != "" && output != "audio" {
			return nil, "", fmt.Errorf("%w: output_modalities for realtime music model %s must be [audio]", ErrInvalidArgument, model)
		}
		return encodeBuildBidiMessage(buildMusicPath, map[string]any{"setup": setup})
	}
	request.variant = bidiVariantConversation
	if err := validateLiveVariant(request); err != nil {
		return nil, "", err
	}
	setup["generationConfig"] = map[string]any{
		"responseModalities": []string{"AUDIO"},
		"speechConfig":       map[string]any{"voiceConfig": map[string]any{"prebuiltVoiceConfig": map[string]any{"voiceName": "Zephyr"}}},
		"mediaResolution":    "MEDIA_RESOLUTION_MEDIUM",
	}
	if len(request.Tools) > 0 {
		tools, _, err := encodeBuildTools(Tools{Functions: request.Tools})
		if err != nil {
			return nil, "", fmt.Errorf("%w: %v", ErrInvalidArgument, err)
		}
		setup["tools"] = tools
	}
	resumption := map[string]any{}
	if token := strings.TrimSpace(request.SessionToken); token != "" {
		resumption["handle"] = token
	}
	setup["sessionResumption"] = resumption
	setup["contextWindowCompression"] = map[string]any{"triggerTokens": "104857", "slidingWindow": map[string]any{"targetTokens": "52428"}}
	setup["inputAudioTranscription"] = map[string]any{}
	setup["outputAudioTranscription"] = map[string]any{}
	return encodeBuildBidiMessage(buildLivePath, map[string]any{"setup": setup})
}

// encodeBuildLiveText encodes text realtime input for Build Live
func encodeBuildLiveText(text string) ([]byte, string, error) {
	if err := validateBidiText(text); err != nil {
		return nil, "", err
	}
	return encodeBuildBidiMessage(buildLivePath, map[string]any{"realtimeInput": map[string]any{"text": text}})
}

// encodeBuildLiveMedia encodes 16 kHz PCM audio or JPEG image realtime input for Build Live
func encodeBuildLiveMedia(mimeType string, data []byte) ([]byte, string, error) {
	mimeType = strings.TrimSpace(mimeType)
	if err := validateBidiMedia(mimeType, data); err != nil {
		return nil, "", err
	}
	blob := map[string]any{"mimeType": mimeType, "data": base64.StdEncoding.EncodeToString(data)}
	field := "video"
	if mimeType == "audio/pcm" {
		blob["mimeType"] = "audio/pcm;rate=16000"
		field = "audio"
	}
	return encodeBuildBidiMessage(buildLivePath, map[string]any{"realtimeInput": map[string]any{field: blob}})
}

// encodeBuildLiveToolResponses encodes function responses for Build Live
func encodeBuildLiveToolResponses(results []FunctionResult) ([]byte, string, error) {
	if err := validateBidiToolResponses(results); err != nil {
		return nil, "", err
	}
	responses := make([]any, 0, len(results))
	for _, result := range results {
		response, err := buildJSONObject(result.Content)
		if err != nil {
			return nil, "", fmt.Errorf("%w: bidi function response content %v", ErrInvalidArgument, err)
		}
		responses = append(responses, map[string]any{"id": result.ID, "name": result.Name, "response": response})
	}
	return encodeBuildBidiMessage(buildLivePath, map[string]any{"toolResponse": map[string]any{"functionResponses": responses}})
}

// webChannelURL returns WebChannel URL used by the session
func (s *BidiSession) webChannelURL() string {
	if s.buildPath != "" {
		return buildBidiWebChannelURL
	}
	return bidiWebChannelURL
}

// bidiProofField returns field index of WAA proof in WebChannel client messages
func bidiProofField(build bool) int {
	if build {
		return buildProofField
	}
	return 6
}

// liveFrame encodes Live client frames per session channel: playground for Playground, build for Build Live, and error for realtime music
func (s *BidiSession) liveFrame(playground, build func() ([]byte, string, error)) ([]byte, string, error) {
	switch s.buildPath {
	case "":
		return playground()
	case buildLivePath:
		return build()
	default:
		return nil, "", fmt.Errorf("%w: realtime music sessions only accept music_prompts, music_config, and playback", ErrInvalidArgument)
	}
}

// parsePayload decodes backchannel payload according to session channel
func (s *BidiSession) parsePayload(raw json.RawMessage) ([]BidiEvent, error) {
	if s.buildPath != "" {
		return ParseBuildBidiServerPayload(raw)
	}
	return ParseBidiServerPayload(raw)
}

// Channel returns upstream channel used by session
func (s *BidiSession) Channel() Channel {
	return s.lease.Channel()
}

// encodeBuildMusicPrompts encodes weighted prompts for realtime music
func encodeBuildMusicPrompts(prompts []WeightedPrompt) ([]byte, string, error) {
	if len(prompts) == 0 {
		return nil, "", fmt.Errorf("%w: weighted_prompts cannot be empty", ErrInvalidArgument)
	}
	for index, prompt := range prompts {
		if strings.TrimSpace(prompt.Text) == "" {
			return nil, "", fmt.Errorf("%w: weighted_prompts[%d].text cannot be empty", ErrInvalidArgument, index)
		}
	}
	return encodeBuildBidiMessage(buildMusicPath, map[string]any{"clientContent": map[string]any{"weightedPrompts": prompts}})
}

// encodeBuildMusicConfig encodes generation config for realtime music matching Gemini API LiveMusicGenerationConfig
func encodeBuildMusicConfig(config json.RawMessage) ([]byte, string, error) {
	var object map[string]any
	if err := json.Unmarshal(config, &object); err != nil || object == nil {
		return nil, "", fmt.Errorf("%w: music_config must be a JSON object", ErrInvalidArgument)
	}
	return encodeBuildBidiMessage(buildMusicPath, map[string]any{"musicGenerationConfig": object})
}

// encodeBuildPlaybackControl encodes playback control for realtime music
func encodeBuildPlaybackControl(control string) ([]byte, string, error) {
	control = strings.ToUpper(strings.TrimSpace(control))
	if !slices.Contains(buildPlaybackControls, control) {
		return nil, "", fmt.Errorf("%w: playback_control must be play, pause, stop, or reset_context", ErrInvalidArgument)
	}
	return encodeBuildBidiMessage(buildMusicPath, map[string]any{"playbackControl": control})
}

// sendMusic validates session as realtime music and sends BidiGenerateMusic client message with user text prompt
func (s *BidiSession) sendMusic(ctx context.Context, text string, encode func() ([]byte, string, error)) error {
	if s.buildPath != buildMusicPath {
		return fmt.Errorf("%w: model %s is not a realtime music model", ErrInvalidArgument, s.model)
	}
	body, binding, err := encode()
	if err != nil {
		return err
	}
	return s.sendProtected(ctx, body, binding, text, false, true)
}

// SendMusicPrompts sends weighted prompts for realtime music
func (s *BidiSession) SendMusicPrompts(ctx context.Context, prompts []WeightedPrompt) error {
	texts := make([]string, 0, len(prompts))
	for _, prompt := range prompts {
		texts = append(texts, prompt.Text)
	}
	return s.sendMusic(ctx, strings.Join(texts, " "), func() ([]byte, string, error) { return encodeBuildMusicPrompts(prompts) })
}

// SendMusicConfig sends generation config for realtime music
func (s *BidiSession) SendMusicConfig(ctx context.Context, config json.RawMessage) error {
	return s.sendMusic(ctx, "", func() ([]byte, string, error) { return encodeBuildMusicConfig(config) })
}

// SendPlaybackControl sends playback control for realtime music
func (s *BidiSession) SendPlaybackControl(ctx context.Context, control string) error {
	return s.sendMusic(ctx, "", func() ([]byte, string, error) { return encodeBuildPlaybackControl(control) })
}

// buildLiveServerMessage represents Gemini Live and realtime music server messages forwarded by Build proxy
type buildLiveServerMessage struct {
	SetupComplete *json.RawMessage `json:"setupComplete"`
	ServerContent *struct {
		ModelTurn *struct {
			Parts []struct {
				Text       *string `json:"text"`
				Thought    bool    `json:"thought"`
				InlineData *struct {
					MIMEType string `json:"mimeType"`
					Data     []byte `json:"data"`
				} `json:"inlineData"`
			} `json:"parts"`
		} `json:"modelTurn"`
		InputTranscription  *BidiTranscription `json:"inputTranscription"`
		OutputTranscription *BidiTranscription `json:"outputTranscription"`
		Interrupted         bool               `json:"interrupted"`
		GenerationComplete  bool               `json:"generationComplete"`
		TurnComplete        bool               `json:"turnComplete"`
		AudioChunks         []struct {
			MIMEType string `json:"mimeType"`
			Data     []byte `json:"data"`
		} `json:"audioChunks"`
	} `json:"serverContent"`
	ToolCall *struct {
		FunctionCalls []struct {
			ID   string          `json:"id"`
			Name string          `json:"name"`
			Args json.RawMessage `json:"args"`
		} `json:"functionCalls"`
	} `json:"toolCall"`
	ToolCallCancellation *struct {
		IDs []string `json:"ids"`
	} `json:"toolCallCancellation"`
	UsageMetadata           json.RawMessage `json:"usageMetadata"`
	GoAway                  json.RawMessage `json:"goAway"`
	SessionResumptionUpdate *struct {
		NewHandle string `json:"newHandle"`
		Resumable bool   `json:"resumable"`
	} `json:"sessionResumptionUpdate"`
}

// ParseBuildBidiServerPayload decodes a WebChannel payload for Build proxy: status, control markers, or ProxyResponse list
func ParseBuildBidiServerPayload(raw json.RawMessage) ([]BidiEvent, error) {
	return parseWebChannelPayload(raw, func(response []json.RawMessage, _ json.RawMessage, path string) ([]BidiEvent, error) {
		text, err := rawString(rawAt(response, 0), path+"[0]", raw)
		if err != nil {
			return nil, withBidiMethod(err)
		}
		return parseBuildLiveServerMessage([]byte(text))
	})
}

// parseBuildLiveServerMessage converts Gemini Live JSON messages into realtime events in Playground decoding order
func parseBuildLiveServerMessage(raw []byte) ([]BidiEvent, error) {
	var message buildLiveServerMessage
	if err := json.Unmarshal(raw, &message); err != nil {
		return nil, &ProtocolEvidenceError{Method: "BidiGenerateContent", Path: "$proxy[0]", Detail: "invalid Gemini Live JSON: " + err.Error(), Raw: cloneRaw(raw)}
	}
	events := make([]BidiEvent, 0, 4)
	if message.SetupComplete != nil {
		events = append(events, BidiEvent{Kind: BidiEventSetupComplete})
	}
	if content := message.ServerContent; content != nil {
		if content.ModelTurn != nil {
			for _, part := range content.ModelTurn.Parts {
				switch {
				case part.Text != nil && part.Thought:
					encoded, _ := json.Marshal(Event{Kind: EventReasoning, Text: *part.Text})
					events = append(events, BidiEvent{Kind: BidiEventProvider, Raw: encoded})
				case part.Text != nil:
					events = append(events, BidiEvent{Kind: BidiEventText, Text: *part.Text})
				case part.InlineData != nil:
					events = append(events, BidiEvent{Kind: BidiEventMedia, Media: &Media{MIME: part.InlineData.MIMEType, Data: part.InlineData.Data}})
				}
			}
		}
		for _, chunk := range content.AudioChunks {
			events = append(events, BidiEvent{Kind: BidiEventMedia, Media: &Media{MIME: chunk.MIMEType, Data: chunk.Data}})
		}
		if content.InputTranscription != nil {
			events = append(events, BidiEvent{Kind: BidiEventInputTranscription, Text: content.InputTranscription.Text, Transcription: content.InputTranscription})
		}
		if content.OutputTranscription != nil {
			events = append(events, BidiEvent{Kind: BidiEventOutputTranscription, Text: content.OutputTranscription.Text, Transcription: content.OutputTranscription})
		}
		for _, flag := range []struct {
			set  bool
			kind BidiEventKind
		}{{content.Interrupted, BidiEventInterrupted}, {content.GenerationComplete, BidiEventGenerationComplete}, {content.TurnComplete, BidiEventTurnComplete}} {
			if flag.set {
				events = append(events, BidiEvent{Kind: flag.kind})
			}
		}
	}
	if message.ToolCall != nil {
		for index, call := range message.ToolCall.FunctionCalls {
			if strings.TrimSpace(call.ID) == "" {
				return nil, &ProtocolEvidenceError{
					Method: "BidiGenerateContent", Path: fmt.Sprintf("$.toolCall.functionCalls[%d].id", index), Detail: "tool call missing call ID", Raw: cloneRaw(raw),
				}
			}
			arguments := bytes.TrimSpace(call.Args)
			if len(arguments) == 0 {
				arguments = []byte("{}")
			}
			events = append(events, BidiEvent{Kind: BidiEventToolCall, ToolCall: &FunctionCall{ID: call.ID, Name: call.Name, Arguments: cloneRaw(arguments)}})
		}
	}
	if message.ToolCallCancellation != nil {
		events = append(events, BidiEvent{Kind: BidiEventToolCallCancellation, ToolCallIDs: message.ToolCallCancellation.IDs})
	}
	if len(message.UsageMetadata) > 0 {
		events = append(events, BidiEvent{Kind: BidiEventUsage, Raw: cloneRaw(message.UsageMetadata)})
	}
	if len(message.GoAway) > 0 {
		events = append(events, BidiEvent{Kind: BidiEventGoAway, Raw: cloneRaw(message.GoAway)})
	}
	if update := message.SessionResumptionUpdate; update != nil {
		events = append(events, BidiEvent{Kind: BidiEventSessionResumption, SessionToken: update.NewHandle, Resumable: update.Resumable})
	}
	if len(events) == 0 {
		events = append(events, BidiEvent{Kind: BidiEventProvider, Raw: cloneRaw(raw)})
	}
	return events, nil
}
