package aistudio

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/google/uuid"
)

const (
	// assistantModelSuffix is the suffix for Build coding assistant model IDs.
	assistantModelSuffix = "-build"
	// assistantOfflineMethod is the protected RPC for submitting Build coding assistant tasks.
	assistantOfflineMethod = "CodeAssistantOffline"
	// assistantStreamMethod is the streaming RPC for reading Build coding assistant tasks.
	assistantStreamMethod = "StreamCodeAssistantOfflineGeneration"
	// assistantUploadMethod is the method name used in assistant file upload errors.
	assistantUploadMethod = "Upload"
	// assistantProofField is the WAA proof field for CodeAssistantOffline.
	assistantProofField = 2
	// assistantDefaultThinkingLevel is the default high thinking level for Build chat.
	assistantDefaultThinkingLevel = 3
	// assistantCleanupTimeout is the deadline for cancelling generation and deleting temporary applets.
	assistantCleanupTimeout = time.Minute
)

// AssistantUploader uploads assistant attachments as an account and returns the upload path.
type AssistantUploader interface {
	UploadAssistantFile(context.Context, string, Blob) (string, error)
}

// assistantModelEntry derives a -build catalog entry from a model available in Build coding assistant.
func assistantModelEntry(base modelEntry) modelEntry {
	model := cloneModels([]Model{base.model})[0]
	model.ID = base.model.ID + assistantModelSuffix
	model.Name = base.model.Name + " (Build)"
	model.Methods = nil
	for _, method := range base.model.Methods {
		if method == "generateContent" || method == "countTokens" {
			model.Methods = append(model.Methods, method)
		}
	}
	model.Capabilities = map[string]bool{"assistant_route": true}
	for _, name := range []string{"chat_model", "thinking", "thinking_level"} {
		if base.model.Capabilities[name] {
			model.Capabilities[name] = true
		}
	}
	model.CapabilityOptions = nil
	return modelEntry{model: model, defaults: GenerationDefaults{
		MaxOutputTokens: base.defaults.MaxOutputTokens, Thinking: true, ThinkingLevel: true,
		DefaultThinkingLevel: assistantDefaultThinkingLevel, ThinkingLevels: base.defaults.ThinkingLevels,
	}}
}

// assistantBaseModelID returns the catalog model ID corresponding to a -build model.
func assistantBaseModelID(modelID string) string {
	return strings.TrimSuffix(strings.TrimPrefix(strings.TrimSpace(modelID), "models/"), assistantModelSuffix)
}

// assistantThinkingLevel selects the thinking level for Build chat from requested reasoning effort or budget; defaults to high when unspecified.
func assistantThinkingLevel(config GenerationConfig, defaults GenerationDefaults) (int64, error) {
	level := int64(assistantDefaultThinkingLevel)
	switch normalizedReasoningEffort(config.ReasoningEffort) {
	case "":
		if config.ThinkingBudget != nil {
			level = thinkingLevelForBudget(*config.ThinkingBudget)
		}
	case "minimal", "none":
		level = 4
	case "low":
		level = 1
	case "medium":
		level = 2
	case "high":
		level = 3
	default:
		return 0, fmt.Errorf("reasoning effort must be none, minimal, low, medium, high, xhigh, or max")
	}
	return closestSupportedThinkingLevel(level, defaults.ThinkingLevels), nil
}

// EncodeAssistantRequest encodes CodeAssistantOffline: field 1 conversation turns, 6 mode, 8 model, 9 custom instructions, 12 applet, 14 framework, 16, 19 thinking config, 29 request ID; field 2 written by WAA proof.
func EncodeAssistantRequest(request GenerateRequest, defaults GenerationDefaults, applet string, requestID string) ([]byte, error) {
	contents, err := encodeContents(request.Contents)
	if err != nil {
		return nil, err
	}
	if len(contents) == 0 {
		return nil, fmt.Errorf("CodeAssistantOffline contents cannot be empty")
	}
	level, err := assistantThinkingLevel(request.Config, defaults)
	if err != nil {
		return nil, err
	}
	wire := make([]any, 29)
	wire[0] = contents
	wire[5] = int64(1)
	wire[7] = wireModelName(assistantBaseModelID(request.Model))
	if request.System != "" {
		wire[8] = request.System
	}
	wire[11] = applet
	wire[13] = []any{int64(2), nil, int64(2)}
	wire[15] = int64(1)
	wire[18] = []any{int64(1), nil, nil, level}
	wire[28] = requestID
	return json.Marshal(wire)
}

// sendAssistant creates a temporary applet and submits a Build coding assistant task, returning the task stream; cancels incomplete tasks and deletes the applet after stream closes.
func (c *Client) sendAssistant(ctx context.Context, request GenerateRequest, entry modelEntry) (*RPCResponse, func(io.Reader, func(Event) error) error, error) {
	reason := ""
	if request.Unary {
		reason = "Build coding assistant uses server-side stream, returned after accumulation"
	}
	reportUpstreamMode(ctx, assistantOfflineMethod, "stream", reason)
	contents, err := c.uploadAssistantAttachments(ctx, request.AccountID, request.Contents)
	if err != nil {
		return nil, nil, err
	}
	request.Contents = contents
	applet, err := c.createAssistantApplet(ctx, request.AccountID)
	if err != nil {
		return nil, nil, err
	}
	cleanup := &assistantCleanup{client: c, accountID: request.AccountID, applet: applet}
	body, err := EncodeAssistantRequest(request, entry.defaults, applet, strings.ToUpper(uuid.NewString()))
	if err != nil {
		cleanup.run()
		return nil, nil, fmt.Errorf("%w: %v", ErrInvalidArgument, err)
	}
	rpc := newRPCRequest(assistantOfflineMethod, request.AccountID, request.ID, body, false)
	c.applyBenefitTier(rpc.Method, request.AccountID, rpc.Header)
	response, err := c.protected.DoProtected(ctx, request, rpc)
	if err == nil {
		response, err = validateRPCResponse(assistantOfflineMethod, response)
	}
	if err != nil {
		cleanup.run()
		return nil, nil, fmt.Errorf("send AI Studio %s: %w", assistantOfflineMethod, err)
	}
	cleanup.generation, err = decodeAssistantGeneration(response.Body)
	response.Body.Close()
	if err != nil {
		cleanup.run()
		return nil, nil, err
	}
	streamBody, err := json.Marshal([]any{cleanup.generation, nil, nil, applet})
	if err != nil {
		cleanup.run()
		return nil, nil, err
	}
	stream, err := c.do(ctx, assistantStreamMethod, request.AccountID, request.ID, streamBody, true)
	if err != nil {
		cleanup.run()
		return nil, nil, err
	}
	stream.Body = &assistantStreamBody{ReadCloser: stream.Body, cleanup: cleanup}
	return stream, func(source io.Reader, emit func(Event) error) error {
		err := DecodeAssistantStream(source, &AssistantStreamDecoder{}, emit)
		if err == nil {
			cleanup.completed.Store(true)
		}
		return err
	}, nil
}

// uploadAssistantAttachments uploads inline attachments in turns as assistant attachment references.
func (c *Client) uploadAssistantAttachments(ctx context.Context, accountID string, contents []Content) ([]Content, error) {
	rewritten := make([]Content, len(contents))
	count := 0
	for index, content := range contents {
		parts := slices.Clone(content.Parts)
		for partIndex := range parts {
			blob := parts[partIndex].InlineData
			if blob == nil {
				continue
			}
			uploader, ok := c.transport.(AssistantUploader)
			if !ok {
				return nil, fmt.Errorf("AI Studio transport does not support assistant file upload")
			}
			count++
			path, err := uploader.UploadAssistantFile(ctx, accountID, *blob)
			if err != nil {
				return nil, fmt.Errorf("upload assistant attachment %d: %w", count, err)
			}
			parts[partIndex].InlineData = nil
			parts[partIndex].Upload = &UploadRef{MIME: blob.MIME, Path: path, Name: fmt.Sprintf("attachment-%d", count), Size: int64(len(blob.Data))}
		}
		rewritten[index] = Content{Role: content.Role, Parts: parts}
	}
	return rewritten, nil
}

// createAssistantApplet creates a temporary applet hosting a coding assistant conversation.
func (c *Client) createAssistantApplet(ctx context.Context, accountID string) (string, error) {
	body, err := json.Marshal([]any{nil, []any{"Untitled", "Generated by Gemini.", nil, nil, "default"}, nil, nil, int64(1)})
	if err != nil {
		return "", err
	}
	response, err := c.do(ctx, "CreateApplet", accountID, "", body, false)
	if err != nil {
		return "", err
	}
	defer response.Body.Close()
	raw, err := io.ReadAll(response.Body)
	if err != nil {
		return "", fmt.Errorf("read AI Studio CreateApplet: %w", err)
	}
	values, err := rawArray(raw, "$", raw)
	if err != nil {
		return "", withMethod(err, "CreateApplet")
	}
	applet, err := rawString(rawAt(values, 0), "$[0]", raw)
	if err != nil || applet == "" {
		return "", &ProtocolEvidenceError{Method: "CreateApplet", Path: "$[0]", Detail: "missing applet ID", Raw: cloneRaw(raw)}
	}
	return applet, nil
}

// decodeAssistantGeneration reads the task ID from CodeAssistantOffline response [taskID, appletID].
func decodeAssistantGeneration(source io.Reader) (string, error) {
	raw, err := io.ReadAll(source)
	if err != nil {
		return "", fmt.Errorf("read AI Studio %s: %w", assistantOfflineMethod, err)
	}
	values, err := rawArray(raw, "$", raw)
	if err != nil {
		return "", withMethod(err, assistantOfflineMethod)
	}
	generation, err := rawString(rawAt(values, 0), "$[0]", raw)
	if err != nil || generation == "" {
		return "", &ProtocolEvidenceError{Method: assistantOfflineMethod, Path: "$[0]", Detail: "missing task ID", Raw: cloneRaw(raw)}
	}
	return generation, nil
}

// assistantCleanup cancels incomplete generation tasks and deletes temporary applets after request completion.
type assistantCleanup struct {
	client     *Client
	accountID  string
	applet     string
	generation string
	completed  atomic.Bool
	once       sync.Once
}

// run executes a single cleanup in the background with an independent timeout.
func (cleanup *assistantCleanup) run() {
	cleanup.once.Do(func() {
		go func() {
			ctx, cancel := context.WithTimeout(context.Background(), assistantCleanupTimeout)
			defer cancel()
			if cleanup.generation != "" && !cleanup.completed.Load() {
				cleanup.call(ctx, "CancelCodeAssistantOfflineGeneration", []any{cleanup.generation, cleanup.applet})
			}
			cleanup.call(ctx, "DeleteApplet", []any{cleanup.applet})
		}()
	})
}

func (cleanup *assistantCleanup) call(ctx context.Context, method string, value []any) {
	body, err := json.Marshal(value)
	if err == nil {
		var response *RPCResponse
		response, err = cleanup.client.do(ctx, method, cleanup.accountID, "", body, false)
		if err == nil {
			_, err = io.Copy(io.Discard, response.Body)
			err = errors.Join(err, response.Body.Close())
		}
	}
	if err != nil {
		slog.Warn("Build coding assistant cleanup failed", "account", cleanup.accountID, "method", method, "applet", cleanup.applet, "error", err)
	}
}

// assistantStreamBody triggers cleanup when the task stream closes.
type assistantStreamBody struct {
	io.ReadCloser
	cleanup *assistantCleanup
}

// Close closes the task stream and cleans up temporary applets.
func (body *assistantStreamBody) Close() error {
	err := body.ReadCloser.Close()
	body.cleanup.run()
	return err
}

// AssistantStreamDecoder decodes coding assistant step snapshots into incremental events.
type AssistantStreamDecoder struct {
	emitted   map[int64][]string
	usage     map[int64]Usage
	lastStep  int64
	wroteText bool
	finished  bool
}

// DecodeAssistantStream decodes StreamCodeAssistantOfflineGeneration response: field 1 is message list, followed optionally by google.rpc status.
func DecodeAssistantStream(source io.Reader, decoder *AssistantStreamDecoder, emit func(Event) error) error {
	reader := &streamReadRecorder{source: source}
	stream := json.NewDecoder(newSparseJSONReader(reader))
	stream.UseNumber()
	index := 0
	fail := func(path string, detail string) error {
		return reader.failure(assistantStreamMethod, index, &ProtocolEvidenceError{Method: assistantStreamMethod, Path: path, Detail: detail})
	}
	if token, err := stream.Token(); err != nil || token != json.Delim('[') {
		return fail("$", "root value is not array")
	}
	if !stream.More() {
		return fail("$", "root array missing message list")
	}
	if token, err := stream.Token(); err != nil || token != json.Delim('[') {
		return fail("$[0]", "message list is not array")
	}
	for ; stream.More(); index++ {
		var raw json.RawMessage
		if err := stream.Decode(&raw); err != nil {
			return fail(fmt.Sprintf("$[0][%d]", index), err.Error())
		}
		events, err := decoder.Decode(raw, fmt.Sprintf("$[0][%d]", index))
		if err != nil {
			return err
		}
		for _, event := range events {
			if err := emit(event); err != nil {
				return err
			}
		}
	}
	if token, err := stream.Token(); err != nil || token != json.Delim(']') {
		return fail("$[0]", "message list did not end properly")
	}
	for stream.More() {
		var raw json.RawMessage
		if err := stream.Decode(&raw); err != nil {
			return fail("$", err.Error())
		}
		if err := assistantTrailerError(raw); err != nil {
			return err
		}
	}
	if err := reader.failure(assistantStreamMethod, index, nil); err != nil {
		return err
	}
	events, err := decoder.End()
	if err != nil {
		return err
	}
	for _, event := range events {
		if err := emit(event); err != nil {
			return err
		}
	}
	return nil
}

// Decode decodes a message [null, null, [step...], appletID]: step field 1 is turns, 6 is failure cause, 12 is index, 13 is status (1 completed, 2 in progress, 4 failed), 16 is usage.
func (d *AssistantStreamDecoder) Decode(raw json.RawMessage, path string) ([]Event, error) {
	if d.emitted == nil {
		d.emitted = make(map[int64][]string)
		d.usage = make(map[int64]Usage)
	}
	message, err := rawArray(raw, path, raw)
	if err != nil {
		return nil, withMethod(err, assistantStreamMethod)
	}
	stepsRaw := rawAt(message, 2)
	if isJSONNull(stepsRaw) {
		return nil, nil
	}
	steps, err := rawArray(stepsRaw, path+"[2]", raw)
	if err != nil {
		return nil, withMethod(err, assistantStreamMethod)
	}
	var events []Event
	for stepIndex, stepRaw := range steps {
		stepPath := fmt.Sprintf("%s[2][%d]", path, stepIndex)
		stepEvents, err := d.decodeStep(stepRaw, stepPath, raw)
		if err != nil {
			return nil, err
		}
		events = append(events, stepEvents...)
	}
	return events, nil
}

func (d *AssistantStreamDecoder) decodeStep(raw json.RawMessage, path string, evidence json.RawMessage) ([]Event, error) {
	step, err := rawArray(raw, path, evidence)
	if err != nil {
		return nil, withMethod(err, assistantStreamMethod)
	}
	number, err := rawInt64(rawAt(step, 11), path+"[11]", evidence)
	if err != nil {
		return nil, withMethod(err, assistantStreamMethod)
	}
	status, err := rawInt64(rawAt(step, 12), path+"[12]", evidence)
	if err != nil {
		return nil, withMethod(err, assistantStreamMethod)
	}
	if failure := rawAt(step, 5); !isJSONNull(failure) {
		values, err := rawArray(failure, path+"[5]", evidence)
		if err != nil {
			return nil, withMethod(err, assistantStreamMethod)
		}
		message, err := rawString(rawAt(values, 0), path+"[5][0]", evidence)
		if err != nil {
			return nil, withMethod(err, assistantStreamMethod)
		}
		return nil, &RPCError{Method: assistantStreamMethod, StatusCode: http.StatusBadGateway, Message: "generation step failed: " + message}
	}
	if usageRaw := rawAt(step, 15); !isJSONNull(usageRaw) && status == 1 {
		usage, err := decodeAssistantUsage(usageRaw, path+"[15]", evidence)
		if err != nil {
			return nil, err
		}
		d.usage[number] = usage
	}
	contentsRaw := rawAt(step, 0)
	if isJSONNull(contentsRaw) {
		return nil, nil
	}
	contents, err := rawArray(contentsRaw, path+"[0]", evidence)
	if err != nil {
		return nil, withMethod(err, assistantStreamMethod)
	}
	var events []Event
	partOffset := 0
	for contentIndex, contentRaw := range contents {
		contentPath := fmt.Sprintf("%s[0][%d]", path, contentIndex)
		content, err := rawArray(contentRaw, contentPath, evidence)
		if err != nil {
			return nil, withMethod(err, assistantStreamMethod)
		}
		role, err := rawString(rawAt(content, 1), contentPath+"[1]", evidence)
		if err != nil {
			return nil, withMethod(err, assistantStreamMethod)
		}
		if role != "model" {
			continue
		}
		parts, err := rawArray(rawAt(content, 0), contentPath+"[0]", evidence)
		if err != nil {
			return nil, withMethod(err, assistantStreamMethod)
		}
		for partIndex, partRaw := range parts {
			partEvents, err := d.decodePart(number, partOffset+partIndex, partRaw, fmt.Sprintf("%s[0][%d]", contentPath, partIndex), evidence)
			if err != nil {
				return nil, err
			}
			events = append(events, partEvents...)
		}
		partOffset += len(parts)
		if status == 1 {
			d.finished = true
		}
	}
	return events, nil
}

// decodePart returns the incremental text for model part relative to previous snapshot.
func (d *AssistantStreamDecoder) decodePart(step int64, index int, raw json.RawMessage, path string, evidence json.RawMessage) ([]Event, error) {
	part, err := rawArray(raw, path, evidence)
	if err != nil {
		return nil, withMethod(err, assistantStreamMethod)
	}
	textRaw := rawAt(part, 1)
	if isJSONNull(textRaw) {
		return nil, nil
	}
	text, err := rawString(textRaw, path+"[1]", evidence)
	if err != nil {
		return nil, withMethod(err, assistantStreamMethod)
	}
	thought := false
	if thoughtRaw := rawAt(part, 12); !isJSONNull(thoughtRaw) {
		thought, err = rawBool(thoughtRaw, path+"[12]", evidence)
		if err != nil {
			return nil, withMethod(err, assistantStreamMethod)
		}
	}
	emitted := d.emitted[step]
	for len(emitted) <= index {
		emitted = append(emitted, "")
	}
	previous := emitted[index]
	if !strings.HasPrefix(text, previous) {
		return nil, &ProtocolEvidenceError{Method: assistantStreamMethod, Path: path + "[1]", Detail: "step text is not a continuation of previous snapshot", Raw: cloneRaw(raw)}
	}
	emitted[index] = text
	d.emitted[step] = emitted
	delta := text[len(previous):]
	if delta == "" {
		return nil, nil
	}
	if thought {
		return []Event{{Kind: EventReasoning, Text: delta}}, nil
	}
	if d.wroteText && previous == "" && step != d.lastStep {
		delta = "\n\n" + delta
	}
	d.wroteText = true
	d.lastStep = step
	return []Event{{Kind: EventText, Text: delta}}, nil
}

// End returns the sum of step usages and completion events when the stream ends.
func (d *AssistantStreamDecoder) End() ([]Event, error) {
	if !d.finished {
		return nil, &ProtocolEvidenceError{Method: assistantStreamMethod, Path: "$", Detail: "stream closed before model reply completed"}
	}
	var total Usage
	for _, usage := range d.usage {
		total.InputTokens += usage.InputTokens
		total.CachedTokens += usage.CachedTokens
		total.OutputTokens += usage.OutputTokens
		total.TotalTokens += usage.TotalTokens
	}
	events := make([]Event, 0, 2)
	if len(d.usage) > 0 {
		events = append(events, Event{Kind: EventUsage, Usage: &total})
	}
	return append(events, Event{Kind: EventFinish, FinishReason: decodeFinishReason(1)}), nil
}

// decodeAssistantUsage decodes step usage [uncached_input, output, cached_input].
func decodeAssistantUsage(raw json.RawMessage, path string, evidence json.RawMessage) (Usage, error) {
	values, err := rawArray(raw, path, evidence)
	if err != nil {
		return Usage{}, withMethod(err, assistantStreamMethod)
	}
	numbers := make([]int64, 3)
	for index := range numbers {
		if value := rawAt(values, index); !isJSONNull(value) {
			numbers[index], err = rawInt64(value, fmt.Sprintf("%s[%d]", path, index), evidence)
			if err != nil {
				return Usage{}, withMethod(err, assistantStreamMethod)
			}
		}
	}
	input := numbers[0] + numbers[2]
	return Usage{InputTokens: input, CachedTokens: numbers[2], OutputTokens: numbers[1], TotalTokens: input + numbers[1]}, nil
}

// assistantTrailerError decodes trailing google.rpc status after message list.
func assistantTrailerError(raw json.RawMessage) error {
	err := buildTrailerError(raw)
	var rpcErr *RPCError
	if errors.As(err, &rpcErr) {
		rpcErr.Method = assistantStreamMethod
	}
	var evidence *ProtocolEvidenceError
	if errors.As(err, &evidence) {
		evidence.Method = assistantStreamMethod
	}
	return err
}
