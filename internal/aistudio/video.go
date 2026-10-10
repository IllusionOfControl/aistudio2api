package aistudio

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"
)

// VideoService defines capabilities required by the long-running video adapter
type VideoService interface {
	GenerateVideo(context.Context, VideoRequest) (VideoOperation, error)
	GetGenerateVideoOperation(context.Context, string) (VideoOperation, error)
	DownloadFile(context.Context, string) (MediaStream, error)
	ListVideos(context.Context) ([]VideoOperation, error)
	DeleteVideo(context.Context, string) error
}

// videoNotFinishedError indicates video operation is not finished and output cannot be deleted yet
type videoNotFinishedError struct {
	operationID string
}

func (err *videoNotFinishedError) Error() string {
	return fmt.Sprintf("video operation %s is not finished yet", err.operationID)
}

func (err *videoNotFinishedError) HTTPStatus() int {
	return http.StatusConflict
}

func (err *videoNotFinishedError) ErrorCode() string {
	return "video_not_ready"
}

// VideoImage represents a Veo start frame
type VideoImage struct {
	InlineData *Blob
	File       *FileRef
}

// VideoRequest represents a Veo long-running operation request
type VideoRequest struct {
	Model             string
	Prompt            string
	AspectRatio       string
	DurationSeconds   int
	Resolution        string
	AccountID         string
	StartImage        *VideoImage
	RecoverWAARuntime func(context.Context, string, error) (bool, error)
}

// VideoOperation represents Veo private long-running operation state
type VideoOperation struct {
	ID              string
	Done            bool
	File            *FileRef
	Model           string
	Seconds         string
	Size            string
	CreatedAt       time.Time
	accessCheckedAt time.Time
	// Missing is upstream error message when operation is not found
	Missing string
}

// ModelAccessCheckedAt returns the qualification time when upstream accepted the video operation
func (operation VideoOperation) ModelAccessCheckedAt() time.Time {
	return operation.accessCheckedAt
}

// EncodeGenerateVideoRequest encodes the current web GenerateVideo array protocol
func EncodeGenerateVideoRequest(request VideoRequest) ([]byte, error) {
	if strings.TrimSpace(request.Model) == "" || strings.TrimSpace(request.Prompt) == "" {
		return nil, fmt.Errorf("GenerateVideo requires model and prompt")
	}
	request = normalizeVideoRequest(request)

	config := []any{
		int64(1),
		request.AspectRatio,
		[]any{strconv.Itoa(request.DurationSeconds)},
		request.Resolution,
	}
	wire := make([]any, 8)
	wire[0] = wireModelName(request.Model)
	wire[1] = request.Prompt
	wire[2] = config
	if request.StartImage != nil {
		inline, file, err := encodeVideoImage(request.StartImage)
		if err != nil {
			return nil, err
		}
		wire[3] = inline
		wire[4] = file
	}
	return json.Marshal(wire)
}

func encodeVideoImage(image *VideoImage) (any, any, error) {
	if image == nil {
		return nil, nil, nil
	}
	if (image.InlineData == nil) == (image.File == nil) {
		return nil, nil, fmt.Errorf("Veo start frame must set exactly one of inline data or Drive file")
	}
	if image.InlineData != nil {
		if !strings.HasPrefix(strings.ToLower(image.InlineData.MIME), "image/") || len(image.InlineData.Data) == 0 {
			return nil, nil, fmt.Errorf("Veo inline start frame requires image MIME and data")
		}
		return []any{image.InlineData.MIME, base64.StdEncoding.EncodeToString(image.InlineData.Data)}, nil, nil
	}
	if strings.TrimSpace(image.File.ID) == "" {
		return nil, nil, fmt.Errorf("Veo Drive start frame missing file ID")
	}
	return nil, []any{image.File.ID}, nil
}

func normalizeVideoRequest(request VideoRequest) VideoRequest {
	if strings.TrimSpace(request.AspectRatio) == "" {
		request.AspectRatio = "16:9"
	}
	if request.DurationSeconds == 0 {
		request.DurationSeconds = 4
	}
	if strings.TrimSpace(request.Resolution) == "" {
		request.Resolution = "720p"
	}
	return request
}

// EncodeGetGenerateVideoOperationRequest encodes the current web polling array protocol
func EncodeGetGenerateVideoOperationRequest(operationID string) ([]byte, error) {
	if strings.TrimSpace(operationID) == "" {
		return nil, fmt.Errorf("GetGenerateVideoOperation requires operation ID")
	}
	return json.Marshal([]any{operationID})
}

// ParseVideoOperation decodes operation returned by GenerateVideo and polling
func ParseVideoOperation(source io.Reader, method string) (VideoOperation, error) {
	raw, err := io.ReadAll(newSparseJSONReader(source))
	if err != nil {
		return VideoOperation{}, fmt.Errorf("read %s: %w", method, err)
	}
	root, err := rawArray(raw, "$", raw)
	if err != nil {
		return VideoOperation{}, withMethod(err, method)
	}
	var operation VideoOperation
	if method == "GenerateVideo" {
		if len(root) > 0 && !isJSONNull(root[0]) {
			operation.ID, err = rawString(root[0], "$[0]", raw)
			if err != nil {
				return VideoOperation{}, withMethod(err, method)
			}
		}
		if operation.ID == "" {
			return VideoOperation{}, &ProtocolEvidenceError{Method: method, Path: "$[0]", Detail: "missing operation ID", Raw: raw}
		}
		return operation, nil
	}
	return decodePolledVideoOperation(operation, root, raw)
}

func decodePolledVideoOperation(operation VideoOperation, root []json.RawMessage, raw json.RawMessage) (VideoOperation, error) {
	if len(root) > 0 && !isJSONNull(root[0]) {
		done, err := rawBool(root[0], "$[0]", raw)
		if err != nil {
			return VideoOperation{}, withMethod(err, "GetGenerateVideoOperation")
		}
		operation.Done = done
	}
	if len(root) > 1 && !isJSONNull(root[1]) {
		results, err := rawArray(root[1], "$[1]", raw)
		if err != nil {
			return VideoOperation{}, withMethod(err, "GetGenerateVideoOperation")
		}
		if len(results) > 0 && !isJSONNull(results[0]) {
			result, err := rawArray(results[0], "$[1][0]", raw)
			if err != nil {
				return VideoOperation{}, withMethod(err, "GetGenerateVideoOperation")
			}
			if len(result) == 0 || isJSONNull(result[0]) {
				return operation, nil
			}
			file, err := rawArray(result[0], "$[1][0][0]", raw)
			if err != nil {
				return VideoOperation{}, withMethod(err, "GetGenerateVideoOperation")
			}
			if len(file) == 0 || isJSONNull(file[0]) {
				return operation, nil
			}
			id, err := rawString(file[0], "$[1][0][0][0]", raw)
			if err != nil {
				return VideoOperation{}, withMethod(err, "GetGenerateVideoOperation")
			}
			operation.File = &FileRef{ID: id, MIME: "video/mp4"}
		}
	}
	return operation, nil
}

// GenerateVideo creates a Veo long-running operation
func (c *Client) GenerateVideo(ctx context.Context, request VideoRequest) (VideoOperation, error) {
	request = normalizeVideoRequest(request)
	entry, err := c.modelEntry(ctx, request.AccountID, request.Model)
	if err != nil {
		return VideoOperation{}, err
	}
	if !hasMethod(entry.model, "predictLongRunning") {
		return VideoOperation{}, fmt.Errorf("%w: live catalog for model %q has no predictLongRunning method", ErrInvalidArgument, entry.model.ID)
	}
	request, err = fitVideoOptions(request, entry.model)
	if err != nil {
		return VideoOperation{}, fmt.Errorf("%w: %v", ErrInvalidArgument, err)
	}
	body, err := EncodeGenerateVideoRequest(request)
	if err != nil {
		return VideoOperation{}, fmt.Errorf("%w: %v", ErrInvalidArgument, err)
	}
	response, err := c.doProtectedVideo(ctx, request, body)
	if err != nil {
		return VideoOperation{}, err
	}
	defer response.Body.Close()
	operation, err := ParseVideoOperation(response.Body, "GenerateVideo")
	if err != nil {
		return VideoOperation{}, err
	}
	operation.accessCheckedAt = time.Now().UTC()
	operation.Seconds = strconv.Itoa(request.DurationSeconds)
	operation.Size = videoOutputSize(request)
	return operation, nil
}

// GetGenerateVideoOperation reads the current status of a Veo long-running operation
func (c *Client) GetGenerateVideoOperation(ctx context.Context, accountID string, operationID string) (VideoOperation, error) {
	body, err := EncodeGetGenerateVideoOperationRequest(operationID)
	if err != nil {
		return VideoOperation{}, err
	}
	response, err := c.do(ctx, "GetGenerateVideoOperation", accountID, "", body, false)
	if err != nil {
		return VideoOperation{}, err
	}
	defer response.Body.Close()
	operation, err := ParseVideoOperation(response.Body, "GetGenerateVideoOperation")
	operation.ID = operationID
	return operation, err
}

// GenerateVideo creates an operation using an exclusive account and saves operation binding
func (s *PooledService) GenerateVideo(ctx context.Context, request VideoRequest) (VideoOperation, error) {
	request = normalizeVideoRequest(request)
	modelID := strings.TrimPrefix(strings.TrimSpace(request.Model), "models/")
	resourceID := ""
	if request.StartImage != nil && request.StartImage.File != nil {
		resourceID = strings.TrimSpace(request.StartImage.File.ID)
	}
	requestedAccountID := strings.TrimSpace(request.AccountID)
	selection := AccountSelection{
		ModelID: modelID, Method: "predictLongRunning", AccountID: requestedAccountID, ResourceID: resourceID,
	}
	maxAttempts := accountAttemptLimit(s.pool, selection.AccountID != "" || selection.ResourceID != "")
	recoveryAccountID := ""
	var operation VideoOperation
	var generateErr error
	for attempt := 0; attempt < maxAttempts; attempt++ {
		selection.AccountID = requestedAccountID
		if recoveryAccountID != "" {
			selection.AccountID = recoveryAccountID
			recoveryAccountID = ""
		}
		lease, owned, err := resolveAccountLease(ctx, s.pool, selection)
		if err != nil {
			if generateErr != nil && errors.Is(err, ErrNoEligibleAccount) {
				return operation, generateErr
			}
			return VideoOperation{}, err
		}
		request.AccountID = lease.Account().ID
		attemptCtx := ContextWithAccountLease(ctx, lease)
		operation, generateErr = s.client.GenerateVideo(attemptCtx, request)
		if generateErr == nil {
			operation.accessCheckedAt = lease.CheckedAt()
			generateErr = errors.Join(
				lease.MarkAuthenticationValid(),
				s.pool.ClearCooldownIfGeneration(
					request.AccountID, "", lease.ModelAccessGeneration(), lease.CheckedAt(),
				),
			)
		}
		if generateErr == nil {
			var binding ResourceBinding
			binding, generateErr = lease.BindVideoOperation(attemptCtx, operation.ID, VideoResourceMetadata{
				Model: request.Model, Seconds: operation.Seconds, Size: operation.Size,
			})
			if generateErr == nil {
				applyVideoOperationBinding(&operation, binding)
			}
		}
		if owned {
			generateErr = errors.Join(generateErr, lease.Release())
		}
		if generateErr == nil || !retryableAccountError(generateErr) {
			if generateErr == nil {
				return operation, nil
			}
			if request.RecoverWAARuntime == nil {
				return operation, generateErr
			}
		}
		if request.RecoverWAARuntime != nil {
			recovered, recoveryErr := request.RecoverWAARuntime(ctx, request.AccountID, generateErr)
			if recoveryErr != nil {
				return operation, errors.Join(generateErr, recoveryErr)
			}
			if recovered {
				recoveryAccountID = request.AccountID
				maxAttempts++
				continue
			}
		}
		if !retryableAccountError(generateErr) {
			return operation, generateErr
		}
		if stateErr := s.markRetryableFailure(lease, modelID, generateErr); stateErr != nil {
			return operation, errors.Join(generateErr, stateErr)
		}
	}
	return operation, generateErr
}

// GetGenerateVideoOperation polls using the operation creator account and binds result file
func (s *PooledService) GetGenerateVideoOperation(ctx context.Context, operationID string) (VideoOperation, error) {
	lease, owned, err := resolveAccountLease(ctx, s.pool, AccountSelection{ResourceID: strings.TrimSpace(operationID)})
	if err != nil {
		return VideoOperation{}, err
	}
	accountID := lease.Account().ID
	binding, bindingErr := lease.VideoOperationBinding(operationID)
	if bindingErr != nil {
		if owned {
			bindingErr = errors.Join(bindingErr, lease.Release())
		}
		return VideoOperation{}, bindingErr
	}
	operation, pollErr := s.client.GetGenerateVideoOperation(ContextWithAccountLease(ctx, lease), accountID, operationID)
	var rpcError *RPCError
	if errors.As(pollErr, &rpcError) && rpcError.StatusCode == http.StatusNotFound {
		operation, pollErr = VideoOperation{ID: operationID, Done: true, Missing: rpcError.Message}, nil
	}
	applyVideoOperationBinding(&operation, binding)
	if pollErr == nil {
		pollErr = errors.Join(
			lease.MarkAuthenticationValid(),
			s.pool.ClearCooldownIfGeneration(
				accountID, "", lease.ModelAccessGeneration(), lease.CheckedAt(),
			),
		)
		if pollErr == nil && operation.Done && operation.File != nil {
			pollErr = lease.BindResource(operation.File.ID, "video-file")
		}
	} else if DefinitiveAuthenticationFailure(pollErr) {
		pollErr = errors.Join(pollErr, lease.MarkAuthenticationRequired(pollErr.Error()))
	}
	if owned {
		pollErr = errors.Join(pollErr, lease.Release())
	}
	return operation, pollErr
}

// ListVideos returns persistent metadata for all video operations
func (s *PooledService) ListVideos(ctx context.Context) ([]VideoOperation, error) {
	return s.pool.ListVideos(ctx)
}

// ListVideos returns video operations ordered newest-to-oldest after refreshing runtime-state for all accounts
func (p *AccountPool) ListVideos(ctx context.Context) ([]VideoOperation, error) {
	if err := p.refreshAllRuntimes(ctx); err != nil {
		return nil, err
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	videos := []VideoOperation{}
	for resourceID, accountID := range p.resources {
		binding := p.byID[accountID].runtime.Resources[resourceID]
		if binding.Kind != "video-operation" || binding.Video == nil {
			continue
		}
		operation := VideoOperation{ID: resourceID}
		applyVideoOperationBinding(&operation, binding)
		videos = append(videos, operation)
	}
	sort.Slice(videos, func(i, j int) bool {
		if !videos[i].CreatedAt.Equal(videos[j].CreatedAt) {
			return videos[i].CreatedAt.After(videos[j].CreatedAt)
		}
		return videos[i].ID < videos[j].ID
	})
	return videos, nil
}

// DeleteVideo deletes Drive result file and binding for completed video operation; returns 409 if not finished
func (s *PooledService) DeleteVideo(ctx context.Context, operationID string) error {
	operationID = strings.TrimSpace(operationID)
	operation, err := s.GetGenerateVideoOperation(ctx, operationID)
	if err != nil {
		return err
	}
	if !operation.Done {
		return &videoNotFinishedError{operationID: operationID}
	}
	if operation.File != nil {
		if err := s.deleteDriveResource(ctx, operation.File.ID); err != nil && !errors.Is(err, ErrResourceNotFound) {
			return err
		}
	}
	return s.pool.unbindResourceContext(ctx, operationID)
}

var _ VideoService = (*PooledService)(nil)

func applyVideoOperationBinding(operation *VideoOperation, binding ResourceBinding) {
	operation.Model = binding.Video.Model
	operation.Seconds = binding.Video.Seconds
	operation.Size = binding.Video.Size
	operation.CreatedAt = binding.CreatedAt
}

func videoOutputSize(request VideoRequest) string {
	width := "1280"
	height := "720"
	switch strings.ToLower(request.Resolution) {
	case "1080p":
		width, height = "1920", "1080"
	case "4k":
		width, height = "3840", "2160"
	}
	if request.AspectRatio == "9:16" {
		return height + "x" + width
	}
	return width + "x" + height
}

// fitVideoOptions snaps duration, aspect ratio, and resolution to nearest model capability options
func fitVideoOptions(request VideoRequest, model Model) (VideoRequest, error) {
	if request.DurationSeconds < 0 {
		return request, fmt.Errorf("video duration cannot be negative")
	}
	if options := model.CapabilityOptions["video_durations_seconds"]; len(options) > 0 {
		request.DurationSeconds, _ = strconv.Atoi(NearestOption(float64(request.DurationSeconds), options, videoDurationSeconds))
	}
	if options := model.CapabilityOptions["video_aspect_ratios"]; len(options) > 0 {
		ratio, ok := AspectRatioLog(request.AspectRatio)
		if !ok {
			return request, fmt.Errorf("cannot parse video aspect ratio %q", request.AspectRatio)
		}
		request.AspectRatio = NearestOption(ratio, options, AspectRatioLog)
	}
	if options := model.CapabilityOptions["video_output_resolutions"]; len(options) > 0 {
		height, ok := videoResolutionHeight(request.Resolution)
		if !ok {
			return request, fmt.Errorf("cannot parse video resolution %q", request.Resolution)
		}
		request.Resolution = NearestOption(height, options, videoResolutionHeight)
	}
	return request, nil
}

// NearestOption returns the option closest in measure to target, choosing the larger one in case of tie
func NearestOption(target float64, options []string, measure func(string) (float64, bool)) string {
	nearest := ""
	nearestDistance, nearestValue := math.Inf(1), math.Inf(-1)
	for _, option := range options {
		value, ok := measure(option)
		if !ok {
			continue
		}
		distance := math.Abs(value - target)
		if distance < nearestDistance || distance == nearestDistance && value > nearestValue {
			nearest, nearestDistance, nearestValue = option, distance, value
		}
	}
	return nearest
}

// AspectRatioLog returns natural logarithm of W:H aspect ratio
func AspectRatioLog(value string) (float64, bool) {
	widthText, heightText, ok := strings.Cut(strings.TrimSpace(value), ":")
	width, widthErr := strconv.ParseFloat(strings.TrimSpace(widthText), 64)
	height, heightErr := strconv.ParseFloat(strings.TrimSpace(heightText), 64)
	if !ok || widthErr != nil || heightErr != nil || !(width > 0) || !(height > 0) {
		return 0, false
	}
	return math.Log(width) - math.Log(height), true
}

// videoDurationSeconds parses seconds from video duration option
func videoDurationSeconds(value string) (float64, bool) {
	seconds, err := strconv.Atoi(strings.TrimSpace(value))
	return float64(seconds), err == nil
}

// videoResolutionHeight returns pixel height for video resolution strings such as 720p or 4k
func videoResolutionHeight(value string) (float64, bool) {
	value = strings.ToLower(strings.TrimSpace(value))
	scale := 0
	switch {
	case strings.HasSuffix(value, "p"):
		scale = 1
	case strings.HasSuffix(value, "k"):
		scale = 540
	}
	number, err := strconv.Atoi(value[:max(len(value)-1, 0)])
	if scale == 0 || err != nil || number <= 0 {
		return 0, false
	}
	return float64(number * scale), true
}
