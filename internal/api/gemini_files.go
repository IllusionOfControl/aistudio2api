package api

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/base32"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/Mag1cFall/AIStudio2API/internal/aistudio"
)

// geminiFilesURIPrefix is prefix for Files API file URIs
const geminiFilesURIPrefix = "https://generativelanguage.googleapis.com/v1beta/files/"

// geminiUploadTTL is idle retention duration for resumable upload sessions between requests
const geminiUploadTTL = time.Hour

// geminiFileNames encodes Drive file ID into alphanumeric resource ID for Files API
var geminiFileNames = base32.StdEncoding.WithPadding(base32.NoPadding)

// geminiFileMetadata represents file metadata carried by create file requests
type geminiFileMetadata struct {
	File struct {
		DisplayName string `json:"displayName"`
		MIMEType    string `json:"mimeType"`
	} `json:"file"`
}

// geminiUploadSession stores metadata and received chunks for a resumable upload
type geminiUploadSession struct {
	mu       sync.Mutex
	name     string
	mime     string
	received int64
	buffer   *os.File
	expires  time.Time
	timer    *time.Timer
	done     bool
}

// geminiUploads manages active resumable upload sessions
type geminiUploads struct {
	mu       sync.Mutex
	sessions map[string]*geminiUploadSession
}

func newGeminiUploads() *geminiUploads {
	return &geminiUploads{sessions: make(map[string]*geminiUploadSession)}
}

// start registers new upload session and returns session ID, pruned after idle TTL
func (uploads *geminiUploads) start(session *geminiUploadSession) string {
	id := rand.Text()
	session.expires = time.Now().Add(geminiUploadTTL)
	session.timer = time.AfterFunc(geminiUploadTTL, func() {
		session.mu.Lock()
		defer session.mu.Unlock()
		if !session.done && !time.Now().Before(session.expires) {
			uploads.discard(id, session)
		}
	})
	uploads.mu.Lock()
	uploads.sessions[id] = session
	uploads.mu.Unlock()
	return id
}

func (uploads *geminiUploads) get(id string) *geminiUploadSession {
	uploads.mu.Lock()
	defer uploads.mu.Unlock()
	return uploads.sessions[id]
}

// discard terminates session and deletes cached chunks; caller must hold lock
func (uploads *geminiUploads) discard(id string, session *geminiUploadSession) {
	uploads.mu.Lock()
	if uploads.sessions[id] == session {
		delete(uploads.sessions, id)
	}
	uploads.mu.Unlock()
	session.done = true
	session.timer.Stop()
	if session.buffer != nil {
		_ = session.buffer.Close()
		_ = os.Remove(session.buffer.Name())
		session.buffer = nil
	}
}

// geminiFileID returns Files API resource ID corresponding to Drive file
func geminiFileID(driveID string) string {
	return strings.ToLower(geminiFileNames.EncodeToString([]byte(driveID)))
}

// geminiDriveID decodes Files API resource ID or files/ resource name issued by this service
func geminiDriveID(name string) (string, bool) {
	decoded, err := geminiFileNames.DecodeString(strings.ToUpper(strings.TrimPrefix(name, "files/")))
	if err != nil || len(decoded) == 0 {
		return "", false
	}
	for _, char := range decoded {
		if !(char >= 'a' && char <= 'z' || char >= 'A' && char <= 'Z' || char >= '0' && char <= '9' || char == '-' || char == '_') {
			return "", false
		}
	}
	return string(decoded), true
}

// geminiUploadedFileID returns Drive file ID corresponding to service-issued Files API URI
func geminiUploadedFileID(uri string) (string, bool) {
	if !geminiFilesAPIURI(uri) {
		return "", false
	}
	parsed, _ := url.Parse(uri)
	_, name, _ := strings.Cut(parsed.Path, "/files/")
	return geminiDriveID(name)
}

// geminiFileObject constructs Files API File resource object
func geminiFileObject(metadata aistudio.FileMetadata) map[string]any {
	id := geminiFileID(metadata.ID)
	created := metadata.CreatedAt.UTC().Format(time.RFC3339Nano)
	return map[string]any{
		"name": "files/" + id, "displayName": metadata.Name, "mimeType": metadata.MIME,
		"sizeBytes": strconv.FormatInt(metadata.Size, 10), "createTime": created, "updateTime": created,
		"uri": geminiFilesURIPrefix + id, "state": "ACTIVE", "source": "UPLOADED",
	}
}

// handleGeminiFileUpload initiates resumable upload or completes multipart upload based on upload headers
func (s *server) handleGeminiFileUpload(w http.ResponseWriter, r *http.Request) {
	service, ok := s.service.(aistudio.FileService)
	if !ok {
		writeGeminiError(w, http.StatusBadRequest, "FAILED_PRECONDITION", "file upload is unavailable")
		return
	}
	protocol := strings.ToLower(strings.TrimSpace(r.Header.Get("X-Goog-Upload-Protocol")))
	if protocol == "" {
		protocol = strings.ToLower(r.URL.Query().Get("uploadType"))
	}
	switch protocol {
	case "resumable":
		s.startGeminiUpload(w, r)
	case "multipart":
		uploadGeminiMultipart(w, r, service)
	default:
		writeGeminiError(w, http.StatusBadRequest, "INVALID_ARGUMENT", "X-Goog-Upload-Protocol must be resumable or multipart")
	}
}

// startGeminiUpload creates resumable upload session, returning upload URL in X-Goog-Upload-URL header
func (s *server) startGeminiUpload(w http.ResponseWriter, r *http.Request) {
	if command := strings.ToLower(strings.TrimSpace(r.Header.Get("X-Goog-Upload-Command"))); command != "start" {
		writeGeminiError(w, http.StatusBadRequest, "INVALID_ARGUMENT", "X-Goog-Upload-Command must be start")
		return
	}
	metadata, err := readGeminiFileMetadata(r.Body)
	if err != nil {
		writeGeminiError(w, http.StatusBadRequest, "INVALID_ARGUMENT", err.Error())
		return
	}
	session := &geminiUploadSession{
		name: geminiUploadName(metadata, "", r),
		mime: firstMediaType(r.Header.Get("X-Goog-Upload-Header-Content-Type"), metadata.File.MIMEType),
	}
	id := s.uploads.start(session)
	w.Header().Set("X-Goog-Upload-URL", requestOrigin(r)+"/upload/v1beta/files?upload_id="+url.QueryEscape(id)+"&upload_protocol=resumable")
	w.Header().Set("X-Goog-Upload-Status", "active")
	w.WriteHeader(http.StatusOK)
}

// geminiUploadHandler routes chunk uploads carrying upload_id via session auth, delegating others
func (s *server) geminiUploadHandler(public http.Handler) http.Handler {
	chunks := bodyLimitMiddleware(http.HandlerFunc(s.handleGeminiUploadChunk))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Has("upload_id") {
			chunks.ServeHTTP(w, r)
			return
		}
		public.ServeHTTP(w, r)
	})
}

// handleGeminiUploadChunk handles resumable upload commands (upload, finalize, query, cancel), uploading on finalize
func (s *server) handleGeminiUploadChunk(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost && r.Method != http.MethodPut {
		w.Header().Set("Allow", "POST, PUT")
		writeGeminiError(w, http.StatusMethodNotAllowed, "INVALID_ARGUMENT", "upload commands require POST or PUT")
		return
	}
	id := r.URL.Query().Get("upload_id")
	session := s.uploads.get(id)
	if session == nil {
		writeGeminiUploadError(w, http.StatusNotFound, "NOT_FOUND", "upload session was not found", "final")
		return
	}
	markAccessLogAuthorized(r.Context())
	session.mu.Lock()
	defer session.mu.Unlock()
	if session.done {
		writeGeminiUploadError(w, http.StatusNotFound, "NOT_FOUND", "upload session was not found", "final")
		return
	}
	session.expires = time.Now().Add(geminiUploadTTL)
	session.timer.Reset(geminiUploadTTL)
	commands := make(map[string]bool)
	for _, command := range strings.Split(strings.ToLower(r.Header.Get("X-Goog-Upload-Command")), ",") {
		commands[strings.TrimSpace(command)] = true
	}
	switch {
	case commands["cancel"]:
		s.uploads.discard(id, session)
		w.Header().Set("X-Goog-Upload-Status", "cancelled")
		w.WriteHeader(http.StatusOK)
		return
	case commands["query"]:
		w.Header().Set("X-Goog-Upload-Status", "active")
		w.Header().Set("X-Goog-Upload-Size-Received", strconv.FormatInt(session.received, 10))
		w.WriteHeader(http.StatusOK)
		return
	case !commands["upload"] && !commands["finalize"]:
		writeGeminiUploadError(w, http.StatusBadRequest, "INVALID_ARGUMENT", "X-Goog-Upload-Command must contain upload, finalize, query or cancel", "active")
		return
	}
	if offset := strings.TrimSpace(r.Header.Get("X-Goog-Upload-Offset")); commands["upload"] && offset != "" && offset != strconv.FormatInt(session.received, 10) {
		writeGeminiUploadError(w, http.StatusBadRequest, "INVALID_ARGUMENT", fmt.Sprintf("X-Goog-Upload-Offset %s does not match the %d bytes received", offset, session.received), "active")
		return
	}
	var content io.Reader = r.Body
	if !commands["upload"] {
		content = http.NoBody
	}
	if !commands["finalize"] || session.buffer != nil {
		if err := bufferGeminiUpload(session, content); err != nil {
			s.uploads.discard(id, session)
			writeGeminiUploadFailure(w, err, "final")
			return
		}
		if !commands["finalize"] {
			w.Header().Set("X-Goog-Upload-Status", "active")
			w.Header().Set("X-Goog-Upload-Size-Received", strconv.FormatInt(session.received, 10))
			w.WriteHeader(http.StatusOK)
			return
		}
		if _, err := session.buffer.Seek(0, io.SeekStart); err != nil {
			s.uploads.discard(id, session)
			writeGeminiUploadError(w, http.StatusInternalServerError, "INTERNAL", err.Error(), "final")
			return
		}
		content = session.buffer
	}
	metadata, err := uploadGeminiFile(r.Context(), s.service.(aistudio.FileService), session.name, session.mime, content)
	s.uploads.discard(id, session)
	if err != nil {
		if shouldWriteRequestError(r, err) {
			writeGeminiUploadFailure(w, err, "final")
		}
		return
	}
	w.Header().Set("X-Goog-Upload-Status", "final")
	writeJSON(w, http.StatusOK, map[string]any{"file": geminiFileObject(metadata)})
}

// bufferGeminiUpload appends chunk to session temporary file
func bufferGeminiUpload(session *geminiUploadSession, content io.Reader) error {
	if session.buffer == nil {
		buffer, err := os.CreateTemp("", "aistudio2api-upload-*")
		if err != nil {
			return err
		}
		session.buffer = buffer
	}
	written, err := io.Copy(session.buffer, io.LimitReader(content, openAIFileMaxBytes-session.received+1))
	session.received += written
	if err != nil {
		return err
	}
	if session.received > openAIFileMaxBytes {
		return &http.MaxBytesError{Limit: openAIFileMaxBytes}
	}
	return nil
}

// uploadGeminiMultipart handles upload with metadata and content in same multipart body
func uploadGeminiMultipart(w http.ResponseWriter, r *http.Request, service aistudio.FileService) {
	mediaType, parameters, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || !strings.HasPrefix(mediaType, "multipart/") || parameters["boundary"] == "" {
		writeGeminiError(w, http.StatusBadRequest, "INVALID_ARGUMENT", "multipart upload requires a multipart Content-Type with boundary")
		return
	}
	reader := multipart.NewReader(r.Body, parameters["boundary"])
	var metadata geminiFileMetadata
	metadataRead := false
	for {
		part, err := reader.NextPart()
		if errors.Is(err, io.EOF) {
			writeGeminiError(w, http.StatusBadRequest, "INVALID_ARGUMENT", "multipart upload requires file content")
			return
		}
		if err != nil {
			writeGeminiUploadFailure(w, fmt.Errorf("%w: %w", aistudio.ErrInvalidArgument, err), "")
			return
		}
		partType, _, _ := mime.ParseMediaType(part.Header.Get("Content-Type"))
		if !metadataRead && part.FileName() == "" && (partType == "application/json" || part.FormName() == "metadata") {
			metadata, err = readGeminiFileMetadata(part)
			_ = part.Close()
			if err != nil {
				writeGeminiError(w, http.StatusBadRequest, "INVALID_ARGUMENT", err.Error())
				return
			}
			metadataRead = true
			continue
		}
		file, err := uploadGeminiFile(r.Context(), service, geminiUploadName(metadata, part.FileName(), r),
			firstMediaType(part.Header.Get("Content-Type"), r.Header.Get("X-Goog-Upload-Header-Content-Type"), metadata.File.MIMEType), part)
		_ = part.Close()
		if err != nil {
			if shouldWriteRequestError(r, err) {
				writeGeminiUploadFailure(w, err, "")
			}
			return
		}
		w.Header().Set("X-Goog-Upload-Status", "final")
		writeJSON(w, http.StatusOK, map[string]any{"file": geminiFileObject(file)})
		return
	}
}

// uploadGeminiFile uploads file content and returns metadata, detecting MIME when undeclared
func uploadGeminiFile(ctx context.Context, service aistudio.FileService, name string, mimeType string, content io.Reader) (aistudio.FileMetadata, error) {
	mediaType, stream, err := multipartStreamMIME(content, mimeType, name)
	if err != nil {
		var maxBytes *http.MaxBytesError
		if errors.As(err, &maxBytes) {
			return aistudio.FileMetadata{}, err
		}
		return aistudio.FileMetadata{}, fmt.Errorf("%w: %w", aistudio.ErrInvalidArgument, err)
	}
	ref, err := service.UploadFile(ctx, aistudio.UploadRequest{
		Name: name, MIME: mediaType, Purpose: defaultFilePurpose, Size: -1, MaxSize: openAIFileMaxBytes, Reader: stream,
	})
	if err != nil {
		return aistudio.FileMetadata{}, err
	}
	return service.FileMetadata(ctx, ref.ID)
}

// readGeminiFileMetadata reads metadata from request body, returning empty metadata when body is empty
func readGeminiFileMetadata(body io.Reader) (geminiFileMetadata, error) {
	var metadata geminiFileMetadata
	raw, err := io.ReadAll(io.LimitReader(body, openAIFileFieldMaxBytes+1))
	if err != nil {
		return metadata, err
	}
	if int64(len(raw)) > openAIFileFieldMaxBytes {
		return metadata, errors.New("file metadata exceeds 64 KB")
	}
	if len(bytes.TrimSpace(raw)) == 0 {
		return metadata, nil
	}
	raw, _, err = geminiCamelKeys(doubleQuotedJSON(raw))
	if err == nil {
		err = json.Unmarshal(raw, &metadata)
	}
	if err != nil {
		return metadata, fmt.Errorf("file metadata: %w", err)
	}
	return metadata, nil
}

// doubleQuotedJSON converts single-quoted strings accepted by Google API into standard double-quoted JSON
func doubleQuotedJSON(raw []byte) []byte {
	if !bytes.ContainsRune(raw, '\'') {
		return raw
	}
	converted := make([]byte, 0, len(raw)+8)
	var quote byte
	for index := 0; index < len(raw); index++ {
		char := raw[index]
		switch {
		case quote == 0:
			if char == '\'' || char == '"' {
				quote = char
				char = '"'
			}
			converted = append(converted, char)
		case char == '\\' && index+1 < len(raw):
			index++
			if quote == '\'' && raw[index] == '\'' {
				converted = append(converted, '\'')
			} else {
				converted = append(converted, char, raw[index])
			}
		case char == quote:
			quote = 0
			converted = append(converted, '"')
		case char == '"':
			converted = append(converted, '\\', '"')
		default:
			converted = append(converted, char)
		}
	}
	return converted
}

// geminiUploadName extracts filename preferring displayName, part filename, then X-Goog-Upload-File-Name
func geminiUploadName(metadata geminiFileMetadata, filename string, r *http.Request) string {
	for _, name := range []string{metadata.File.DisplayName, filename, r.Header.Get("X-Goog-Upload-File-Name")} {
		if name = strings.TrimSpace(name); name != "" {
			return name
		}
	}
	return "file"
}

// firstMediaType returns first declared specific media type
func firstMediaType(values ...string) string {
	for _, value := range values {
		if mediaType := declaredMediaType(value); mediaType != "" {
			return mediaType
		}
	}
	return ""
}

// geminiUploadFailure returns HTTP status and message for upload failure, 413 for oversized files
func geminiUploadFailure(err error) (int, string) {
	var maxBytes *http.MaxBytesError
	if errors.As(err, &maxBytes) || statusFromError(err) == http.StatusRequestEntityTooLarge {
		return http.StatusRequestEntityTooLarge, "INVALID_ARGUMENT"
	}
	return statusFromError(err), geminiErrorStatus(err)
}

// writeGeminiUploadError writes upload error response with X-Goog-Upload-Status header
func writeGeminiUploadError(w http.ResponseWriter, status int, statusName string, message string, uploadStatus string) {
	w.Header().Set("X-Goog-Upload-Status", uploadStatus)
	writeGeminiError(w, status, statusName, message)
}

// writeGeminiUploadFailure writes upload failure error, setting X-Goog-Upload-Status when provided
func writeGeminiUploadFailure(w http.ResponseWriter, err error, uploadStatus string) {
	status, statusName := geminiUploadFailure(err)
	if uploadStatus != "" {
		w.Header().Set("X-Goog-Upload-Status", uploadStatus)
	}
	writeGeminiError(w, status, statusName, err.Error())
}

// requestOrigin returns client protocol and host accessing this service
func requestOrigin(r *http.Request) string {
	scheme := "http"
	if r.TLS != nil {
		scheme = "https"
	}
	if forwarded := strings.TrimSpace(r.Header.Get("X-Forwarded-Proto")); forwarded != "" {
		scheme = forwarded
	}
	return scheme + "://" + r.Host
}

// handleGeminiFileGet returns File resource object for uploaded file
func (s *server) handleGeminiFileGet(w http.ResponseWriter, r *http.Request) {
	service, ok := s.service.(aistudio.FileService)
	if !ok {
		writeGeminiError(w, http.StatusBadRequest, "FAILED_PRECONDITION", "file metadata is unavailable")
		return
	}
	driveID, ok := geminiDriveID(r.PathValue("file"))
	if !ok {
		writeGeminiFileNotFound(w, r.PathValue("file"))
		return
	}
	metadata, err := service.FileMetadata(r.Context(), driveID)
	if err != nil {
		writeGeminiFileError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, geminiFileObject(metadata))
}

// handleGeminiFileDelete deletes uploaded file and returns empty object
func (s *server) handleGeminiFileDelete(w http.ResponseWriter, r *http.Request) {
	service, ok := s.service.(aistudio.FileService)
	if !ok {
		writeGeminiError(w, http.StatusBadRequest, "FAILED_PRECONDITION", "file deletion is unavailable")
		return
	}
	driveID, ok := geminiDriveID(r.PathValue("file"))
	if !ok {
		writeGeminiFileNotFound(w, r.PathValue("file"))
		return
	}
	if err := service.DeleteFile(r.Context(), driveID); err != nil {
		writeGeminiFileError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{})
}

// handleGeminiFileList lists uploaded files paginated newest-to-oldest with pageToken
func (s *server) handleGeminiFileList(w http.ResponseWriter, r *http.Request) {
	service, ok := s.service.(aistudio.FileService)
	if !ok {
		writeGeminiError(w, http.StatusBadRequest, "FAILED_PRECONDITION", "file listing is unavailable")
		return
	}
	query := r.URL.Query()
	size, offset := 10, 0
	if value := firstQueryValue(query, "pageSize", "page_size"); value != "" {
		parsed, err := strconv.Atoi(value)
		if err != nil || parsed < 0 {
			writeGeminiError(w, http.StatusBadRequest, "INVALID_ARGUMENT", "pageSize must be a non-negative integer")
			return
		}
		if parsed > 0 {
			size = min(parsed, 100)
		}
	}
	if value := firstQueryValue(query, "pageToken", "page_token"); value != "" {
		parsed, err := strconv.Atoi(value)
		if err != nil || parsed < 0 {
			writeGeminiError(w, http.StatusBadRequest, "INVALID_ARGUMENT", "pageToken is invalid")
			return
		}
		offset = parsed
	}
	files, err := service.ListFiles(r.Context())
	if err != nil {
		if shouldWriteRequestError(r, err) {
			writeGeminiError(w, statusFromError(err), geminiErrorStatus(err), err.Error())
		}
		return
	}
	page := files[min(offset, len(files)):min(offset+size, len(files))]
	objects := make([]map[string]any, 0, len(page))
	for _, file := range page {
		objects = append(objects, geminiFileObject(file))
	}
	body := map[string]any{"files": objects}
	if offset+size < len(files) {
		body["nextPageToken"] = strconv.Itoa(offset + size)
	}
	writeJSON(w, http.StatusOK, body)
}

// firstQueryValue returns first non-empty query parameter, accepting camelCase and snake_case
func firstQueryValue(query url.Values, names ...string) string {
	for _, name := range names {
		if value := query.Get(name); value != "" {
			return value
		}
	}
	return ""
}

// writeGeminiFileError writes file query or deletion error, returning 404 if file not found
func writeGeminiFileError(w http.ResponseWriter, r *http.Request, err error) {
	if !shouldWriteRequestError(r, err) {
		return
	}
	if errors.Is(err, aistudio.ErrResourceNotFound) {
		writeGeminiFileNotFound(w, r.PathValue("file"))
		return
	}
	writeGeminiError(w, statusFromError(err), geminiErrorStatus(err), err.Error())
}

// writeGeminiFileNotFound writes 404 file not found error
func writeGeminiFileNotFound(w http.ResponseWriter, name string) {
	writeGeminiError(w, http.StatusNotFound, "NOT_FOUND", fmt.Sprintf("file %q was not found", strings.TrimPrefix(name, "files/")))
}
