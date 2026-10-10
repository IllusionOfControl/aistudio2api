package api

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/Mag1cFall/AIStudio2API/internal/aistudio"
)

const (
	openAIFileMaxBytes        int64 = 512 << 20
	openAIFileRequestOverhead int64 = 1 << 20
	openAIFileFieldMaxBytes   int64 = 64 << 10
)

type openAIFileUploadResult struct {
	ref aistudio.FileRef
	err error
}

type openAIFilePurposeResult struct {
	value string
	err   error
}

type completionReader struct {
	reader io.Reader
	done   chan error
	closed bool
}

func (reader *completionReader) Read(target []byte) (int, error) {
	count, err := reader.reader.Read(target)
	if err != nil && !reader.closed {
		reader.closed = true
		reader.done <- err
	}
	return count, err
}

type openAIFileObject struct {
	ID        string `json:"id"`
	Object    string `json:"object"`
	Bytes     int64  `json:"bytes"`
	CreatedAt int64  `json:"created_at"`
	Filename  string `json:"filename"`
	Purpose   string `json:"purpose"`
	Status    string `json:"status"`
}

// handleFileUpload uploads a file, returning Files API metadata for Anthropic or OpenAI File object otherwise
func (s *server) handleFileUpload(w http.ResponseWriter, r *http.Request) {
	service, ok := s.service.(aistudio.FileService)
	if !ok {
		writeFileError(w, r, http.StatusBadRequest, "invalid_request", "file upload is unavailable")
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, openAIFileMaxBytes+openAIFileRequestOverhead)
	reader, err := r.MultipartReader()
	if err != nil {
		writeFileParseError(w, r, err)
		return
	}
	purpose, file, filename, contentType, err := readOpenAIFileParts(reader)
	if err != nil {
		writeFileParseError(w, r, err)
		return
	}
	defer file.Close()
	mimeType, stream, err := multipartStreamMIME(file, contentType, filename)
	if err != nil {
		writeFileError(w, r, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	request := aistudio.UploadRequest{
		Name: filename, MIME: mimeType, Purpose: purpose, Size: -1, MaxSize: openAIFileMaxBytes, Reader: stream,
	}
	var ref aistudio.FileRef
	if purpose != "" {
		ref, err = service.UploadFile(r.Context(), request)
	} else {
		ref, err = uploadOpenAIFileBeforePurpose(r.Context(), service, reader, request)
	}
	if err != nil {
		if !shouldWriteRequestError(r, err) {
			return
		}
		var maxBytes *http.MaxBytesError
		if errors.As(err, &maxBytes) {
			writeFileParseError(w, r, err)
			return
		}
		writeFileError(w, r, statusFromError(err), openAIErrorCode(err), err.Error())
		return
	}
	metadata, err := service.FileMetadata(r.Context(), ref.ID)
	if err != nil {
		if !shouldWriteRequestError(r, err) {
			return
		}
		writeFileError(w, r, statusFromError(err), openAIErrorCode(err), err.Error())
		return
	}
	writeJSON(w, http.StatusOK, fileResponse(r, metadata))
}

func readOpenAIFileParts(reader *multipart.Reader) (string, *multipart.Part, string, string, error) {
	purpose := ""
	for {
		part, err := reader.NextPart()
		if errors.Is(err, io.EOF) {
			return "", nil, "", "", errors.New("file is required")
		}
		if err != nil {
			return "", nil, "", "", err
		}
		if part.FormName() == "file" && part.FileName() != "" {
			filename := strings.TrimSpace(part.FileName())
			if filename == "" {
				_ = part.Close()
				return "", nil, "", "", errors.New("filename is required")
			}
			return purpose, part, filename, part.Header.Get("Content-Type"), nil
		}
		value, err := readOpenAIFileField(part)
		_ = part.Close()
		if err != nil {
			return "", nil, "", "", err
		}
		if part.FormName() == "purpose" && purpose == "" {
			purpose = strings.TrimSpace(value)
		}
	}
}

func readOpenAIFileField(part *multipart.Part) (string, error) {
	data, err := io.ReadAll(io.LimitReader(part, openAIFileFieldMaxBytes+1))
	if err != nil {
		return "", err
	}
	if int64(len(data)) > openAIFileFieldMaxBytes {
		return "", errors.New("multipart field exceeds 64 KB")
	}
	return string(data), nil
}

func uploadOpenAIFileBeforePurpose(
	ctx context.Context,
	service aistudio.FileService,
	reader *multipart.Reader,
	request aistudio.UploadRequest,
) (aistudio.FileRef, error) {
	uploadCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	consumed := make(chan error, 1)
	request.Reader = &completionReader{reader: request.Reader, done: consumed}
	purpose := make(chan openAIFilePurposeResult, 1)
	request.ResolvePurpose = func(resolveCtx context.Context) (string, error) {
		select {
		case result := <-purpose:
			return result.value, result.err
		case <-resolveCtx.Done():
			return "", resolveCtx.Err()
		}
	}
	done := make(chan openAIFileUploadResult, 1)
	go func() {
		ref, err := service.UploadFile(uploadCtx, request)
		done <- openAIFileUploadResult{ref: ref, err: err}
	}()
	select {
	case result := <-done:
		return result.ref, result.err
	case readErr := <-consumed:
		if !errors.Is(readErr, io.EOF) {
			result := <-done
			return result.ref, result.err
		}
	}
	value, parseErr := readOpenAIFilePurpose(reader)
	purpose <- openAIFilePurposeResult{value: value, err: parseErr}
	result := <-done
	if parseErr != nil {
		return result.ref, parseErr
	}
	return result.ref, result.err
}

// defaultFilePurpose is default purpose when purpose is omitted
const defaultFilePurpose = "user_data"

// readOpenAIFilePurpose reads purpose following file part, defaulting when empty or absent
func readOpenAIFilePurpose(reader *multipart.Reader) (string, error) {
	for {
		part, err := reader.NextPart()
		if errors.Is(err, io.EOF) {
			return defaultFilePurpose, nil
		}
		if err != nil {
			if errors.Is(err, context.Canceled) {
				return "", err
			}
			return "", fmt.Errorf("%w: %w", aistudio.ErrInvalidArgument, err)
		}
		value, readErr := readOpenAIFileField(part)
		_ = part.Close()
		if readErr != nil {
			if errors.Is(readErr, context.Canceled) {
				return "", readErr
			}
			return "", fmt.Errorf("%w: %w", aistudio.ErrInvalidArgument, readErr)
		}
		if part.FormName() == "purpose" {
			if value = strings.TrimSpace(value); value == "" {
				return defaultFilePurpose, nil
			}
			return value, nil
		}
	}
}

// handleFileGet returns metadata for an uploaded file
func (s *server) handleFileGet(w http.ResponseWriter, r *http.Request) {
	service, ok := s.service.(aistudio.FileService)
	if !ok {
		writeFileError(w, r, http.StatusBadRequest, "invalid_request", "file metadata is unavailable")
		return
	}
	metadata, err := service.FileMetadata(r.Context(), strings.TrimSpace(r.PathValue("file")))
	if err != nil {
		writeFileRequestError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, fileResponse(r, metadata))
}

// handleFileContent returns the uploaded file content.
func (s *server) handleFileContent(w http.ResponseWriter, r *http.Request) {
	service, ok := s.service.(aistudio.FileService)
	if !ok {
		writeFileError(w, r, http.StatusBadRequest, "invalid_request", "file content is unavailable")
		return
	}
	fileID := strings.TrimSpace(r.PathValue("file"))
	metadata, err := service.FileMetadata(r.Context(), fileID)
	if err != nil {
		writeFileRequestError(w, r, err)
		return
	}
	media, err := service.DownloadFile(r.Context(), fileID)
	if err != nil {
		writeFileRequestError(w, r, err)
		return
	}
	mimeType := strings.TrimSpace(media.MIME)
	if mimeType == "" {
		mimeType = metadata.MIME
	}
	filename := strings.TrimSpace(media.Name)
	if filename == "" {
		filename = metadata.Name
	}
	size := media.Size
	if size < 0 {
		size = metadata.Size
	}
	w.Header().Set("Content-Type", mimeType)
	w.Header().Set("Content-Disposition", mime.FormatMediaType("attachment", map[string]string{"filename": filename}))
	if size >= 0 {
		w.Header().Set("Content-Length", strconv.FormatInt(size, 10))
	}
	w.WriteHeader(http.StatusOK)
	_, copyErr := io.Copy(w, media.Body)
	closeErr := media.Body.Close()
	if requestErr := errors.Join(copyErr, closeErr); requestErr != nil {
		SetAccessLogError(r.Context(), requestErr)
	}
}

// handleFileDelete deletes an uploaded file.
func (s *server) handleFileDelete(w http.ResponseWriter, r *http.Request) {
	service, ok := s.service.(aistudio.FileService)
	if !ok {
		writeFileError(w, r, http.StatusBadRequest, "invalid_request", "file deletion is unavailable")
		return
	}
	fileID := strings.TrimSpace(r.PathValue("file"))
	if err := service.DeleteFile(r.Context(), fileID); err != nil {
		writeFileRequestError(w, r, err)
		return
	}
	if anthropicFilesRequest(r) {
		writeJSON(w, http.StatusOK, map[string]any{"id": fileID, "type": "file_deleted"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"id": fileID, "object": "file", "deleted": true})
}

// handleFileList lists uploaded files with Anthropic or OpenAI pagination parameters
func (s *server) handleFileList(w http.ResponseWriter, r *http.Request) {
	service, ok := s.service.(aistudio.FileService)
	if !ok {
		writeFileError(w, r, http.StatusBadRequest, "invalid_request", "file listing is unavailable")
		return
	}
	files, err := service.ListFiles(r.Context())
	if err != nil {
		writeFileRequestError(w, r, err)
		return
	}
	if anthropicFilesRequest(r) {
		writeAnthropicFileList(w, r, files)
		return
	}
	query := r.URL.Query()
	if purpose := query.Get("purpose"); purpose != "" {
		files = slices.DeleteFunc(files, func(file aistudio.FileMetadata) bool { return file.Purpose != purpose })
	}
	listed, hasMore, err := openAIListPage(query, files, func(file aistudio.FileMetadata) string { return file.ID }, listLimits{fallback: 10000, min: 1, max: 10000})
	if err != nil {
		writeOpenAIError(w, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	data := make([]openAIFileObject, 0, len(listed))
	for _, file := range listed {
		data = append(data, openAIFileResponse(file))
	}
	body := map[string]any{"object": "list", "data": data, "has_more": hasMore, "first_id": nil, "last_id": nil}
	if len(listed) > 0 {
		body["first_id"], body["last_id"] = listed[0].ID, listed[len(listed)-1].ID
	}
	writeJSON(w, http.StatusOK, body)
}

// anthropicFilePagePrefix is prefix for Files API next_page cursor, remaining part being last file ID
const anthropicFilePagePrefix = "page_"

// writeAnthropicFileList returns paginated files matching Anthropic Files API parameters
func writeAnthropicFileList(w http.ResponseWriter, r *http.Request, files []aistudio.FileMetadata) {
	query := r.URL.Query()
	if query.Get("scope_id") != "" {
		files = nil
	}
	var listed []aistudio.FileMetadata
	hasMore := false
	if ids := strings.Split(strings.Join(append(query["ids"], query["ids[]"]...), ","), ","); slices.ContainsFunc(ids, func(id string) bool { return id != "" }) {
		listed = slices.DeleteFunc(files, func(file aistudio.FileMetadata) bool { return !slices.Contains(ids, file.ID) })
	} else {
		if page := query.Get("page"); page != "" {
			query.Set("after_id", strings.TrimPrefix(page, anthropicFilePagePrefix))
		}
		var err error
		listed, hasMore, err = anthropicListPage(query, files, func(file aistudio.FileMetadata) string { return file.ID }, listLimits{fallback: 20, min: 1, max: 1000})
		if err != nil {
			writeAnthropicError(w, http.StatusBadRequest, "invalid_request_error", err.Error())
			return
		}
	}
	data := make([]any, 0, len(listed))
	for _, file := range listed {
		data = append(data, fileResponse(r, file))
	}
	body := map[string]any{"data": data, "next_page": nil, "has_more": hasMore, "first_id": nil, "last_id": nil}
	if len(listed) > 0 {
		body["first_id"], body["last_id"] = listed[0].ID, listed[len(listed)-1].ID
	}
	if hasMore && query.Get("before_id") == "" {
		body["next_page"] = anthropicFilePagePrefix + listed[len(listed)-1].ID
	}
	writeJSON(w, http.StatusOK, body)
}

func multipartStreamMIME(file io.Reader, value string, filename string) (string, io.Reader, error) {
	probe := make([]byte, 512)
	count, readErr := io.ReadFull(file, probe)
	if readErr != nil && !errors.Is(readErr, io.EOF) && !errors.Is(readErr, io.ErrUnexpectedEOF) {
		return "", nil, readErr
	}
	if count == 0 {
		return "", nil, errors.New("file must not be empty")
	}
	stream := io.MultiReader(bytes.NewReader(probe[:count]), file)
	if mediaType := declaredMediaType(value); mediaType != "" {
		return mediaType, stream, nil
	}
	return detectMediaType(filename, probe[:count]), stream, nil
}

func multipartFileMIME(file io.ReadSeeker, value string, filename string) (string, error) {
	if mediaType := declaredMediaType(value); mediaType != "" {
		return mediaType, nil
	}
	probe := make([]byte, 512)
	count, err := file.Read(probe)
	if err != nil && !errors.Is(err, io.EOF) {
		return "", err
	}
	if _, err := file.Seek(0, io.SeekStart); err != nil {
		return "", err
	}
	return detectMediaType(filename, probe[:count]), nil
}

// declaredMediaType returns declared specific media type, empty when missing, subtype-less, or generic binary
func declaredMediaType(value string) string {
	mediaType, _, _ := mime.ParseMediaType(value)
	if !strings.Contains(mediaType, "/") || mediaType == "application/octet-stream" {
		return ""
	}
	return mediaType
}

// audioExtensionTypes maps file extensions accepted by OpenAI transcription to MIME types
var audioExtensionTypes = map[string]string{
	".flac": "audio/flac", ".m4a": "audio/mp4", ".mp3": "audio/mpeg", ".mp4": "video/mp4", ".mpeg": "audio/mpeg",
	".mpga": "audio/mpeg", ".ogg": "audio/ogg", ".wav": "audio/wav", ".webm": "audio/webm",
}

func openAIFileResponse(metadata aistudio.FileMetadata) openAIFileObject {
	return openAIFileObject{
		ID: metadata.ID, Object: "file", Bytes: metadata.Size, CreatedAt: metadata.CreatedAt.Unix(),
		Filename: metadata.Name, Purpose: metadata.Purpose, Status: "processed",
	}
}

// anthropicFilesRequest checks whether request originates from Anthropic Files API client
func anthropicFilesRequest(r *http.Request) bool {
	return r.Header.Get("Anthropic-Version") != "" || strings.Contains(r.Header.Get("Anthropic-Beta"), "files-api-")
}

// fileResponse returns Anthropic file metadata or OpenAI file object based on request protocol
func fileResponse(r *http.Request, metadata aistudio.FileMetadata) any {
	if !anthropicFilesRequest(r) {
		return openAIFileResponse(metadata)
	}
	return map[string]any{
		"id": metadata.ID, "type": "file", "filename": metadata.Name, "mime_type": metadata.MIME,
		"size_bytes": metadata.Size, "created_at": metadata.CreatedAt.UTC().Format(time.RFC3339), "downloadable": true,
	}
}

// writeFileError writes file endpoint error according to request protocol
func writeFileError(w http.ResponseWriter, r *http.Request, status int, code string, message string) {
	if anthropicFilesRequest(r) {
		writeAnthropicError(w, status, anthropicStatusErrorType(status), message)
		return
	}
	writeOpenAIError(w, status, code, message)
}

// writeFileRequestError writes file service error, returning 404 when file not found
func writeFileRequestError(w http.ResponseWriter, r *http.Request, err error) {
	if !shouldWriteRequestError(r, err) {
		return
	}
	if errors.Is(err, aistudio.ErrResourceNotFound) {
		writeFileError(w, r, http.StatusNotFound, "file_not_found", "file not found")
		return
	}
	writeFileError(w, r, statusFromError(err), openAIErrorCode(err), err.Error())
}

// writeFileParseError writes multipart parsing error, returning 413 when size limit exceeded
func writeFileParseError(w http.ResponseWriter, r *http.Request, err error) {
	var maxBytes *http.MaxBytesError
	if errors.As(err, &maxBytes) {
		writeFileError(w, r, http.StatusRequestEntityTooLarge, "file_too_large", "file exceeds 512 MB")
		return
	}
	writeFileError(w, r, http.StatusBadRequest, "invalid_request", err.Error())
}

// listLimits defines default and range for list limit parameter
type listLimits struct {
	fallback, min, max int
}

// listLimit reads limit query parameter, falling back to default when absent
func listLimit(query url.Values, limits listLimits) (int, error) {
	raw := query.Get("limit")
	if raw == "" {
		return limits.fallback, nil
	}
	value, err := strconv.Atoi(raw)
	if err != nil || value < limits.min || value > limits.max {
		return 0, fmt.Errorf("limit must be an integer between %d and %d", limits.min, limits.max)
	}
	return value, nil
}

// openAIListPage returns paginated items matching OpenAI order, after, and limit parameters
func openAIListPage[T any](query url.Values, items []T, id func(T) string, limits listLimits) ([]T, bool, error) {
	limit, err := listLimit(query, limits)
	if err != nil {
		return nil, false, err
	}
	switch query.Get("order") {
	case "", "desc":
	case "asc":
		items = slices.Clone(items)
		slices.Reverse(items)
	default:
		return nil, false, errors.New("order must be asc or desc")
	}
	start := 0
	if after := query.Get("after"); after != "" {
		start = len(items)
		if index := slices.IndexFunc(items, func(item T) bool { return id(item) == after }); index >= 0 {
			start = index + 1
		}
	}
	end := min(start+limit, len(items))
	return items[start:end], end < len(items), nil
}

// anthropicListPage returns paginated items matching Anthropic cursor parameters
func anthropicListPage[T any](query url.Values, items []T, id func(T) string, limits listLimits) ([]T, bool, error) {
	limit, err := listLimit(query, limits)
	if err != nil {
		return nil, false, err
	}
	cursor := func(value string) int { return slices.IndexFunc(items, func(item T) bool { return id(item) == value }) }
	if before := query.Get("before_id"); before != "" {
		end := max(cursor(before), 0)
		start := max(end-limit, 0)
		return items[start:end], start > 0, nil
	}
	start := 0
	if after := query.Get("after_id"); after != "" {
		start = len(items)
		if index := cursor(after); index >= 0 {
			start = index + 1
		}
	}
	end := min(start+limit, len(items))
	return items[start:end], end < len(items), nil
}
