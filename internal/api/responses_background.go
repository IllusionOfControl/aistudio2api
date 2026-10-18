package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"sync"
	"time"

	"github.com/Mag1cFall/AIStudio2API/internal/aistudio"
)

// backgroundRun holds the cancel function and completion signal for an in-progress background response.
type backgroundRun struct {
	cancel context.CancelFunc
	done   chan struct{}
}

// startBackground registers a background response and returns a context that does not cancel on client disconnect; finish is called upon completion.
func (s *server) startBackground(r *http.Request, id string) (context.Context, func()) {
	DetachAccessLog(r.Context())
	ctx, cancel := context.WithCancel(context.WithoutCancel(r.Context()))
	run := backgroundRun{cancel: cancel, done: make(chan struct{})}
	s.background.Store(id, run)
	return ctx, func() {
		s.background.Delete(id)
		cancel()
		close(run.done)
	}
}

// storeBackgroundEnd saves the final status of a background response: cancelled when stopped via cancel API, failed on generation error.
func (s *server) storeBackgroundEnd(ctx context.Context, id string, created int64, request responsesRequest, prepared responsesPreparation, err error) {
	switch {
	case errors.Is(ctx.Err(), context.Canceled):
		s.storeResponse(id, request, prepared, generationResult{}, responseShell(id, created, "cancelled", request))
	case err != nil:
		failed := responseShell(id, created, "failed", request)
		failed["error"] = map[string]any{"code": responsesErrorCode(err), "message": err.Error()}
		s.storeResponse(id, request, prepared, generationResult{}, failed)
	}
}

// respondBackground stores and returns a response object in queued state, then completes generation within this request: returns Content-Length and Connection: close so clients receive response immediately; transitions to in_progress when generation starts, and stores final object upon completion.
func (s *server) respondBackground(w http.ResponseWriter, r *http.Request, request responsesRequest, prepared responsesPreparation, id string) {
	ctx, finish := s.startBackground(r, id)
	defer finish()
	created := time.Now().Unix()
	queued := responseShell(id, created, "queued", request)
	s.storeResponse(id, request, prepared, generationResult{}, queued)
	body, _ := json.Marshal(queued)
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Content-Length", strconv.Itoa(len(body)))
	w.Header().Set("Connection", "close")
	_, _ = w.Write(body)
	_ = http.NewResponseController(w).Flush()

	compaction, err := s.compactOverThreshold(ctx, &request, &prepared, id)
	var events <-chan aistudio.Event
	if err == nil {
		generate := prepared.generate
		generate.Unary = true
		events, err = s.service.Generate(ctx, generate)
	}
	var result generationResult
	if err == nil {
		s.storeResponse(id, request, prepared, generationResult{}, responseShell(id, created, "in_progress", request))
		result, err = consumeEvents(ctx, events, nil)
	}
	var response map[string]any
	if err == nil {
		response, err = buildResponsesObject(id, created, request, result, compaction)
	}
	if err == nil && ctx.Err() == nil {
		s.storeResponse(id, request, prepared, result, response)
		return
	}
	s.storeBackgroundEnd(ctx, id, created, request, prepared, err)
}

// streamBackground sends a streaming response in background mode: events progress through queued response.created/response.queued, in_progress, output items, and terminal events, all recorded in the event log; this request follows events from start, continuing generation if client disconnects, supporting GET ?stream=true&starting_after= resumption; cancels to cancelled on cancel API, writes response.failed on other errors.
func (s *server) streamBackground(w http.ResponseWriter, r *http.Request, request responsesRequest, prepared responsesPreparation, id string) {
	ctx, finish := s.startBackground(r, id)
	stream := newStreamLog()
	prepared.stream = stream
	following := make(chan struct{})
	go func() {
		defer close(following)
		if streamHeaders(w) == nil {
			_ = stream.follow(r.Context(), w, -1)
		}
	}()
	defer func() {
		stream.finish()
		<-following
		finish()
	}()
	created := time.Now().Unix()
	queued := responseShell(id, created, "queued", request)
	s.storeResponse(id, request, prepared, generationResult{}, queued)
	writer := newResponsesWriter(stream, id, created, request)
	_ = writer.emit("response.created", map[string]any{"response": queued})
	_ = writer.emit("response.queued", map[string]any{"response": queued})

	compaction, err := s.compactOverThreshold(ctx, &request, &prepared, id)
	prepared.stream = stream
	var events <-chan aistudio.Event
	if err == nil {
		events, err = s.service.Generate(ctx, prepared.generate)
	}
	reported := func(error) bool { return !errors.Is(ctx.Err(), context.Canceled) }
	if err == nil {
		inProgress := responseShell(id, created, "in_progress", request)
		s.storeResponse(id, request, prepared, generationResult{}, inProgress)
		_ = writer.emit("response.in_progress", map[string]any{"response": inProgress})
		err = s.streamOutput(ctx, writer, prepared, events, compaction, reported)
	} else if reported(err) {
		_ = writer.failed(err)
	}
	if err != nil || ctx.Err() != nil {
		s.storeBackgroundEnd(ctx, id, created, request, prepared, err)
	}
}

// streamLog stores event frames emitted by background streaming responses, indexed by sequence_number; acts as http.ResponseWriter receiving frames from writer, excluding heartbeat comment frames.
type streamLog struct {
	header http.Header
	mu     sync.Mutex
	frames [][]byte
	done   bool
	wake   chan struct{}
}

// newStreamLog returns an empty event log.
func newStreamLog() *streamLog {
	return &streamLog{header: http.Header{}, wake: make(chan struct{})}
}

// Header returns non-transmitted response headers.
func (l *streamLog) Header() http.Header { return l.header }

// WriteHeader ignores status codes.
func (l *streamLog) WriteHeader(int) {}

// Flush satisfies flusher calls from writers.
func (l *streamLog) Flush() {}

// Write records an event frame and wakes followers.
func (l *streamLog) Write(frame []byte) (int, error) {
	if bytes.HasPrefix(frame, []byte(":")) {
		return len(frame), nil
	}
	l.mu.Lock()
	l.frames = append(l.frames, bytes.Clone(frame))
	close(l.wake)
	l.wake = make(chan struct{})
	l.mu.Unlock()
	return len(frame), nil
}

// finish marks all events as written and wakes followers.
func (l *streamLog) finish() {
	l.mu.Lock()
	if !l.done {
		l.done = true
		close(l.wake)
		l.wake = make(chan struct{})
	}
	l.mu.Unlock()
}

// follow writes event frames after sequence number after to client, following until completion, client disconnect, or write failure; emits comment frames at heartbeat intervals while waiting.
func (l *streamLog) follow(ctx context.Context, w http.ResponseWriter, after int) error {
	next := max(after+1, 0)
	heartbeat := time.NewTimer(streamHeartbeatInterval)
	defer heartbeat.Stop()
	for {
		l.mu.Lock()
		frames, done, wake := l.frames[min(next, len(l.frames)):], l.done, l.wake
		l.mu.Unlock()
		for _, frame := range frames {
			if _, err := w.Write(frame); err != nil {
				return err
			}
			next++
		}
		if len(frames) > 0 {
			if err := http.NewResponseController(w).Flush(); err != nil {
				return err
			}
		}
		if done {
			return nil
		}
		heartbeat.Reset(streamHeartbeatInterval)
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-wake:
		case <-heartbeat.C:
			if err := writeSSEHeartbeat(w); err != nil {
				return err
			}
		}
	}
}
