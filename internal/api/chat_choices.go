package api

import (
	"context"
	"fmt"
	"sync"

	"github.com/Mag1cFall/AIStudio2API/internal/aistudio"
)

// maxChatChoices is the maximum value for Chat completion n
const maxChatChoices = 128

// chatChoices manages n concurrent generations for a request; if any generation fails, the entire request fails with first error
type chatChoices struct {
	ctx     context.Context
	cancel  context.CancelFunc
	events  []<-chan aistudio.Event
	err     error
	once    sync.Once
	running sync.WaitGroup
}

// startChatChoices launches each choice in separate goroutines; unary requests wait for all to return, streaming waits for first ready choice
func (s *server) startChatChoices(ctx context.Context, request aistudio.GenerateRequest, count int, stream bool) *chatChoices {
	choices := &chatChoices{events: make([]<-chan aistudio.Event, count)}
	choices.ctx, choices.cancel = context.WithCancel(ctx)
	started := make(chan error, count)
	for index := range count {
		choice := request
		if index > 0 {
			choice.ID = fmt.Sprintf("%s-%d", request.ID, index)
			choice.Contents = cloneResponseContents(request.Contents)
			if request.Config.Seed != nil {
				seed := *request.Config.Seed + int64(index)
				choice.Config.Seed = &seed
			}
		}
		events := make(chan aistudio.Event)
		choices.events[index] = events
		choices.running.Add(1)
		go func() {
			defer choices.running.Done()
			upstream, err := s.service.Generate(choices.ctx, choice)
			source := upstream
			if err == nil && stream {
				source, err = awaitStreamStart(choices.ctx, upstream)
			}
			started <- err
			if err != nil {
				failed := make(chan aistudio.Event, 1)
				failed <- aistudio.Event{Kind: aistudio.EventError, Err: err}
				close(failed)
				source = failed
			}
			relayEvents(choices.ctx, source, events)
			if upstream != nil {
				for range upstream {
				}
			}
		}()
	}
	waits := count
	if stream {
		waits = 1
	}
	for range waits {
		if err := <-started; err != nil {
			choices.fail(err)
			break
		}
	}
	return choices
}

// relayEvents forwards source events to out channel via memory queue
func relayEvents(ctx context.Context, source <-chan aistudio.Event, out chan<- aistudio.Event) {
	var queue []aistudio.Event
	for source != nil || len(queue) > 0 {
		var send chan<- aistudio.Event
		var next aistudio.Event
		if len(queue) > 0 {
			send, next = out, queue[0]
		}
		select {
		case <-ctx.Done():
			return
		case send <- next:
			queue = queue[1:]
		case event, ok := <-source:
			if !ok {
				source = nil
				continue
			}
			queue = append(queue, event)
		}
	}
	close(out)
}

// fail records the first error and cancels other generations
func (choices *chatChoices) fail(err error) {
	choices.once.Do(func() {
		choices.err = err
		choices.cancel()
	})
}

// run concurrently consumes choice event streams and returns results by index
func (choices *chatChoices) run(consume func(int, <-chan aistudio.Event) (generationResult, error)) ([]generationResult, error) {
	results := make([]generationResult, len(choices.events))
	var wait sync.WaitGroup
	for index, events := range choices.events {
		wait.Add(1)
		go func() {
			defer wait.Done()
			result, err := consume(index, events)
			if err != nil {
				choices.fail(err)
			}
			results[index] = result
		}()
	}
	wait.Wait()
	return results, choices.err
}

// settle cancels unfinished generations, waits for all to exit when n > 1, and records first error
func (choices *chatChoices) settle(ctx context.Context) {
	choices.cancel()
	if len(choices.events) < 2 {
		return
	}
	choices.running.Wait()
	SetAccessLogError(ctx, choices.err)
}

// record records aggregate usage and tool calls across multiple choices in access log
func (choices *chatChoices) record(ctx context.Context, results []generationResult) {
	if len(choices.events) < 2 {
		return
	}
	toolCalls := 0
	for index := range results {
		toolCalls += len(results[index].toolCalls)
	}
	SetAccessLogGenerationResult(ctx, sumUsage(results), toolCalls)
}

// sumUsage returns aggregate usage across choices, returning nil when all lack usage
func sumUsage(results []generationResult) *aistudio.Usage {
	var total *aistudio.Usage
	for index := range results {
		usage := results[index].usage
		if usage == nil {
			continue
		}
		if total == nil {
			total = &aistudio.Usage{}
		}
		total.InputTokens += usage.InputTokens
		total.OutputTokens += usage.OutputTokens
		total.ReasoningTokens += usage.ReasoningTokens
		total.ToolTokens += usage.ToolTokens
		total.TotalTokens += usage.TotalTokens
		total.OutputTokensMissing = total.OutputTokensMissing || usage.OutputTokensMissing
	}
	return total
}
