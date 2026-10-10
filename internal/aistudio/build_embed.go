package aistudio

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"strings"
)

// EmbedContentRequest represents a single embedding input and parameters
type EmbedContentRequest struct {
	Content              Content
	TaskType             string
	Title                string
	OutputDimensionality *int64
}

// EmbeddingRequest defines an embedding call via Build channel
type EmbeddingRequest struct {
	ID                    string
	Model                 string
	Requests              []EmbedContentRequest
	AccountID             string
	CandidateAccountIDs   []string
	RecoverWAARuntime     func(context.Context, string, error) (bool, error)
	ObserveAccountFailure func(string, error)
}

// EmbeddingResult stores vectors in input order and upstream token counts
type EmbeddingResult struct {
	Embeddings [][]float64
	TokenCount int64
}

// EmbeddingService defines capabilities required for public embedding endpoints
type EmbeddingService interface {
	Embed(context.Context, EmbeddingRequest) (EmbeddingResult, error)
}

// BuildProtectedTransport atomically performs fresh WAA proof generation, field 3 injection, and in-context transmission for Build proxy requests; string parameter is prompt written to page
type BuildProtectedTransport interface {
	DoProtectedBuild(context.Context, AccountSelection, string, RPCRequest) (*RPCResponse, error)
}

var _ BuildProtectedTransport = (*WorkerProtectedTransport)(nil)
var _ EmbeddingService = (*PooledService)(nil)

// buildEmbedContentRequest represents an item in batchEmbedContents matching official SDK field ordering
type buildEmbedContentRequest struct {
	Content              buildEmbedContent `json:"content"`
	TaskType             string            `json:"taskType,omitempty"`
	Title                string            `json:"title,omitempty"`
	OutputDimensionality *int64            `json:"outputDimensionality,omitempty"`
	Model                string            `json:"model"`
}

type buildEmbedContent struct {
	Role  string `json:"role"`
	Parts []any  `json:"parts"`
}

// EncodeBuildEmbedRequest encodes Gemini API path and request body for batchEmbedContents, with model name per item
func EncodeBuildEmbedRequest(model string, requests []EmbedContentRequest) (string, []byte, error) {
	wire := make([]buildEmbedContentRequest, 0, len(requests))
	for index, request := range requests {
		if len(request.Content.Parts) == 0 {
			return "", nil, fmt.Errorf("embedding input %d has no content", index)
		}
		parts := make([]any, 0, len(request.Content.Parts))
		for partIndex, part := range request.Content.Parts {
			encoded, err := encodeBuildPart(part)
			if err != nil {
				return "", nil, fmt.Errorf("encode embedding input %d part %d: %w", index, partIndex, err)
			}
			parts = append(parts, encoded)
		}
		wire = append(wire, buildEmbedContentRequest{
			Content: buildEmbedContent{Role: "user", Parts: parts}, TaskType: request.TaskType, Title: request.Title,
			OutputDimensionality: request.OutputDimensionality, Model: "models/" + model,
		})
	}
	body, err := json.Marshal(map[string]any{"requests": wire})
	if err != nil {
		return "", nil, fmt.Errorf("encode embedding request: %w", err)
	}
	return "/v1beta/models/" + model + ":batchEmbedContents", body, nil
}

// DecodeBuildEmbedResponse decodes BatchEmbedContentsResponse from ProxyUnaryCall, verifying vector count matches request count
func DecodeBuildEmbedResponse(source io.Reader, expected int) (EmbeddingResult, error) {
	raw, err := io.ReadAll(source)
	if err != nil {
		return EmbeddingResult{}, fmt.Errorf("read AI Studio %s: %w", buildProxyUnaryMethod, err)
	}
	decoded, err := decodeJSONValue(raw)
	if err != nil {
		return EmbeddingResult{}, &ProtocolEvidenceError{Method: buildProxyUnaryMethod, Path: "$", Detail: err.Error(), Raw: cloneRaw(raw)}
	}
	content, err := buildProxyResponseBody(decoded, "$")
	if err != nil {
		return EmbeddingResult{}, err
	}
	var response struct {
		Embeddings []struct {
			Values []float64 `json:"values"`
		} `json:"embeddings"`
		TokenCount json.Number `json:"tokenCount"`
	}
	if err := json.Unmarshal(content, &response); err != nil {
		return EmbeddingResult{}, &ProtocolEvidenceError{Method: buildProxyUnaryMethod, Path: "$embeddings", Detail: err.Error(), Raw: cloneRaw(content)}
	}
	if len(response.Embeddings) != expected {
		return EmbeddingResult{}, &ProtocolEvidenceError{
			Method: buildProxyUnaryMethod, Path: "$embeddings",
			Detail: fmt.Sprintf("returned %d vectors, requested %d items", len(response.Embeddings), expected), Raw: cloneRaw(content),
		}
	}
	result := EmbeddingResult{Embeddings: make([][]float64, 0, expected)}
	for _, embedding := range response.Embeddings {
		result.Embeddings = append(result.Embeddings, embedding.Values)
	}
	if response.TokenCount != "" {
		if result.TokenCount, err = response.TokenCount.Int64(); err != nil {
			return EmbeddingResult{}, &ProtocolEvidenceError{Method: buildProxyUnaryMethod, Path: "$tokenCount", Detail: err.Error(), Raw: cloneRaw(content)}
		}
	}
	return result, nil
}

// DoProtectedBuild writes prompt to page, attaches fresh proof, and sends Build proxy request via worker; binding is path and body
func (t *WorkerProtectedTransport) DoProtectedBuild(ctx context.Context, selection AccountSelection, page string, rpc RPCRequest) (*RPCResponse, error) {
	prompt, err := buildBindingPrompt(rpc.Body)
	if err != nil {
		return nil, err
	}
	return t.doBrowserPrepared(ctx, prompt, page, buildProofField, selection, rpc)
}

// buildEmbedBatchLimit is the maximum request items accepted in a single batchEmbedContents call
const buildEmbedBatchLimit = 100

// embed sends batchEmbedContents chunks up to batch limit over current Build lease and merges results in input order
func (c *Client) embed(ctx context.Context, request EmbeddingRequest) (EmbeddingResult, error) {
	transport, ok := c.protected.(BuildProtectedTransport)
	if !ok {
		return EmbeddingResult{}, fmt.Errorf("AI Studio protected transport does not support Build proxy requests")
	}
	var result EmbeddingResult
	for start := 0; start < len(request.Requests); start += buildEmbedBatchLimit {
		batch, err := c.embedBatch(ctx, transport, request, request.Requests[start:min(start+buildEmbedBatchLimit, len(request.Requests))])
		if err != nil {
			return EmbeddingResult{}, err
		}
		result.Embeddings = append(result.Embeddings, batch.Embeddings...)
		result.TokenCount += batch.TokenCount
	}
	return result, nil
}

// embedBatch sends a single batchEmbedContents request
func (c *Client) embedBatch(ctx context.Context, transport BuildProtectedTransport, request EmbeddingRequest, requests []EmbedContentRequest) (EmbeddingResult, error) {
	path, body, err := EncodeBuildEmbedRequest(request.Model, requests)
	if err != nil {
		return EmbeddingResult{}, fmt.Errorf("%w: %v", ErrInvalidArgument, err)
	}
	proxy, err := EncodeBuildProxyRequest(path, body, true)
	if err != nil {
		return EmbeddingResult{}, fmt.Errorf("encode Build proxy request: %w", err)
	}
	rpc := newRPCRequest(buildProxyUnaryMethod, request.AccountID, request.ID, proxy, false)
	c.applyBenefitTier(rpc.Method, request.AccountID, rpc.Header)
	selection := AccountSelection{ModelID: request.Model, Method: "embedContent", AccountID: request.AccountID}
	inputs := make([]Content, 0, len(requests))
	for _, item := range requests {
		inputs = append(inputs, item.Content)
	}
	response, err := transport.DoProtectedBuild(ctx, selection, contentsText(inputs), rpc)
	if err != nil {
		return EmbeddingResult{}, fmt.Errorf("send AI Studio %s: %w", buildProxyUnaryMethod, err)
	}
	response, err = validateRPCResponse(buildProxyUnaryMethod, response)
	if err != nil {
		return EmbeddingResult{}, err
	}
	defer response.Body.Close()
	return DecodeBuildEmbedResponse(response.Body, len(requests))
}

// Embed calls batchEmbedContents on Build channel of eligible accounts, logging build:<model> cooldown on quota errors and retrying
func (s *PooledService) Embed(ctx context.Context, request EmbeddingRequest) (EmbeddingResult, error) {
	modelID := strings.TrimPrefix(strings.TrimSpace(request.Model), "models/")
	if modelID == "" {
		return EmbeddingResult{}, fmt.Errorf("%w: embedding model cannot be empty", ErrInvalidArgument)
	}
	request.Model = modelID
	selection := AccountSelection{ModelID: modelID, Method: "embedContent"}
	maxAttempts := accountAttemptLimit(s.pool, false)
	attempted := make(map[string]struct{}, maxAttempts)
	recoveryAccountID := ""
	var requestErr error
	for attempt := 0; attempt < maxAttempts; attempt++ {
		selection.AllowedAccountIDs = remainingTranscriptionCandidates(request.CandidateAccountIDs, attempted)
		if request.CandidateAccountIDs != nil && len(selection.AllowedAccountIDs) == 0 {
			break
		}
		selection.AccountID = recoveryAccountID
		recoveryAccountID = ""
		lease, _, err := resolveAccountLease(ctx, s.pool, selection)
		if err != nil {
			if requestErr != nil && errors.Is(err, ErrNoEligibleAccount) {
				return EmbeddingResult{}, requestErr
			}
			return EmbeddingResult{}, err
		}
		accountID := lease.Account().ID
		attempted[accountID] = struct{}{}
		request.AccountID = accountID
		result, err := s.client.embed(ContextWithAccountLease(ctx, lease), request)
		if err == nil {
			accessGeneration, checkedAt, scope := lease.ModelAccessGeneration(), lease.CheckedAt(), lease.CooldownScope(modelID)
			if stateErr := errors.Join(lease.MarkAuthenticationValid(), lease.Release()); stateErr != nil {
				return result, stateErr
			}
			go func() {
				if _, err := s.pool.MarkModelAccessVerifiedIfGeneration(accountID, scope, accessGeneration, checkedAt); err != nil {
					slog.Error("failed to save account embedding eligibility", "account", accountID, "model", modelID, "error", err)
				}
			}()
			return result, nil
		}
		requestErr = err
		retryable := retryableAccountError(err)
		var stateErr error
		if retryable {
			stateErr = s.markRetryableFailure(lease, lease.CooldownScope(modelID), err)
		}
		if releaseErr := lease.Release(); stateErr != nil || releaseErr != nil {
			return EmbeddingResult{}, errors.Join(requestErr, stateErr, releaseErr)
		}
		if request.RecoverWAARuntime != nil {
			recovered, recoveryErr := request.RecoverWAARuntime(ctx, accountID, err)
			if recoveryErr != nil {
				return EmbeddingResult{}, errors.Join(requestErr, recoveryErr)
			}
			if recovered {
				delete(attempted, accountID)
				recoveryAccountID = accountID
				maxAttempts++
				continue
			}
		}
		if (!retryable && !errors.Is(err, ErrAccountLeased)) || ctx.Err() != nil {
			return EmbeddingResult{}, requestErr
		}
		if request.ObserveAccountFailure != nil {
			request.ObserveAccountFailure(accountID, err)
		}
	}
	if requestErr != nil {
		return EmbeddingResult{}, requestErr
	}
	return EmbeddingResult{}, ErrNoEligibleAccount
}
