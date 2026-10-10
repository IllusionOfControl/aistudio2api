package api

import (
	"encoding/json"
	"fmt"
	"maps"
	"net/http"
	"slices"
	"strconv"

	"github.com/Mag1cFall/AIStudio2API/internal/aistudio"
)

// interactionRecord stores resource object and input steps for a saved interaction
type interactionRecord struct {
	object json.RawMessage
	input  []json.RawMessage
}

// interactionEvent represents a streaming event during interaction replay
type interactionEvent struct {
	kind  string
	value map[string]any
}

// storeInteraction saves continuation context and retrievable interaction resources
func (s *server) storeInteraction(request interactionRequest, previous responseHistory, current []aistudio.Content, result generationResult, object map[string]any) {
	stored := maps.Clone(object)
	if _, ok := stored["steps"]; !ok {
		steps, err := interactionSteps(result, request.audioFormat(), request.Generation.ThinkingSummaries != "none")
		if err != nil {
			return
		}
		stored["steps"] = steps
	}
	encoded, err := json.Marshal(stored)
	if err != nil {
		return
	}
	id, _ := object["id"].(string)
	interaction := &interactionRecord{object: encoded, input: interactionInputSteps(request.Input)}
	s.responseStates.Store(id, previous, result, responseState{Contents: current, Interaction: interaction})
}

// interactionInputSteps normalizes request inputs into steps, merging consecutive content into user_input
func interactionInputSteps(raw json.RawMessage) []json.RawMessage {
	var text string
	if json.Unmarshal(raw, &text) == nil {
		step, _ := json.Marshal(map[string]any{"type": "user_input", "content": []map[string]any{{"type": "text", "text": text}}})
		return []json.RawMessage{step}
	}
	items, _ := interactionList[json.RawMessage](raw)
	steps := make([]json.RawMessage, 0, len(items))
	var content []json.RawMessage
	flush := func() {
		if len(content) > 0 {
			step, _ := json.Marshal(map[string]any{"type": "user_input", "content": content})
			steps = append(steps, step)
			content = nil
		}
	}
	for _, item := range items {
		var head struct {
			Type string `json:"type"`
		}
		_ = json.Unmarshal(item, &head)
		switch head.Type {
		case "text", "image", "audio", "video", "document":
			content = append(content, item)
		default:
			flush()
			steps = append(steps, item)
		}
	}
	flush()
	return steps
}

// handleInteractionGet returns saved interaction, including inputs when requested or replaying as SSE stream
func (s *server) handleInteractionGet(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	record, ok := s.responseStates.Interaction(id)
	if !ok {
		writeInteractionNotFound(w, id)
		return
	}
	query := r.URL.Query()
	if stream, _ := strconv.ParseBool(query.Get("stream")); stream {
		replayInteraction(w, r, record.object, query.Get("last_event_id"))
		return
	}
	if include, _ := strconv.ParseBool(query.Get("include_input")); include && len(record.input) > 0 {
		var fields map[string]json.RawMessage
		var steps []json.RawMessage
		_ = json.Unmarshal(record.object, &fields)
		_ = json.Unmarshal(fields["steps"], &steps)
		fields["steps"], _ = json.Marshal(append(slices.Clone(record.input), steps...))
		writeJSON(w, http.StatusOK, fields)
		return
	}
	writeJSON(w, http.StatusOK, record.object)
}

// handleInteractionDelete deletes saved interaction, invalidating future previous_interaction_id use
func (s *server) handleInteractionDelete(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if !s.responseStates.Delete(id, true) {
		writeInteractionNotFound(w, id)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{})
}

// handleInteractionCancel returns saved interaction object
func (s *server) handleInteractionCancel(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	record, ok := s.responseStates.Interaction(id)
	if !ok {
		writeInteractionNotFound(w, id)
		return
	}
	writeJSON(w, http.StatusOK, record.object)
}

// replayInteraction replays saved interaction events after last_event_id matching original stream format
func replayInteraction(w http.ResponseWriter, r *http.Request, object json.RawMessage, lastEventID string) {
	var fields map[string]json.RawMessage
	var steps []map[string]json.RawMessage
	_ = json.Unmarshal(object, &fields)
	_ = json.Unmarshal(fields["steps"], &steps)
	delete(fields, "steps")
	created := maps.Clone(fields)
	created["status"] = json.RawMessage(`"in_progress"`)
	delete(created, "usage")
	events := []interactionEvent{{"interaction.created", map[string]any{"interaction": created}}}
	for index, step := range steps {
		var kind string
		_ = json.Unmarshal(step["type"], &kind)
		switch kind {
		case "model_output":
			var content []json.RawMessage
			_ = json.Unmarshal(step["content"], &content)
			events = append(events, interactionEvent{"step.start", map[string]any{"index": index, "step": map[string]any{"type": kind, "content": []any{}}}})
			for _, item := range content {
				events = append(events, interactionEvent{"step.delta", map[string]any{"index": index, "delta": item}})
			}
		case "thought":
			var summary []json.RawMessage
			_ = json.Unmarshal(step["summary"], &summary)
			events = append(events, interactionEvent{"step.start", map[string]any{"index": index, "step": map[string]any{"type": kind, "summary": []any{}}}})
			for _, item := range summary {
				events = append(events, interactionEvent{"step.delta", map[string]any{"index": index, "delta": map[string]any{"type": "thought_summary", "content": item}}})
			}
			if signature, ok := step["signature"]; ok {
				events = append(events, interactionEvent{"step.delta", map[string]any{"index": index, "delta": map[string]any{"type": "thought_signature", "signature": signature}}})
			}
		default:
			events = append(events, interactionEvent{"step.start", map[string]any{"index": index, "step": step}})
		}
		events = append(events, interactionEvent{"step.stop", map[string]any{"index": index}})
	}
	events = append(events, interactionEvent{"interaction.completed", map[string]any{"interaction": fields}})
	start := 0
	if lastEventID != "" {
		sequence, err := strconv.Atoi(lastEventID)
		if err != nil || sequence < 1 || sequence > len(events) {
			writeGeminiError(w, http.StatusBadRequest, "INVALID_ARGUMENT", fmt.Sprintf("last_event_id %q was not found", lastEventID))
			return
		}
		start = sequence
	}
	if err := streamHeaders(w); err != nil {
		return
	}
	for sequence := start; sequence < len(events); sequence++ {
		event := events[sequence]
		event.value["event_type"] = event.kind
		event.value["event_id"] = strconv.Itoa(sequence + 1)
		if err := writeSSE(w, event.kind, event.value); err != nil {
			SetAccessLogError(r.Context(), err)
			return
		}
	}
}

// writeInteractionNotFound writes 404 error when interaction is not found
func writeInteractionNotFound(w http.ResponseWriter, id string) {
	writeGeminiError(w, http.StatusNotFound, "NOT_FOUND", fmt.Sprintf("interaction %q was not found", id))
}
