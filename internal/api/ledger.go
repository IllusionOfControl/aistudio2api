package api

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"
)

// requestBodyLimit is maximum captured bytes stored per request or response body
const requestBodyLimit = 64 << 10

// usageMaxSpan is maximum query time window allowed for usage statistics
const usageMaxSpan = 93 * 24 * time.Hour

// usageMaxBuckets is maximum number of buckets allowed for usage query
const usageMaxBuckets = 1500

// ErrRequestBodyNotFound indicates request body was not captured
var ErrRequestBodyNotFound = errors.New("request body not found")

// RequestLedger provides request usage statistics, record logs, and optional captured bodies
type RequestLedger interface {
	BodyCapture() bool
	SaveBody(RequestBody)
	Usage(context.Context, UsageQuery) (UsageReport, error)
	Records(context.Context, UsageRecordQuery) (UsageRecordPage, error)
	ExportRecords(context.Context, UsageRecordQuery, io.Writer) error
	RequestBody(context.Context, string) (RequestBody, error)
}

// UsageDimensions lists dimensions available for filtering and grouping usage
var UsageDimensions = []string{"model", "account", "channel", "protocol", "state"}

// UsageFilters filters requests by dimension, multiple values within same dimension being OR
type UsageFilters map[string][]string

// UsageQuery defines time range, bucket, timezone, filters, and stacking dimension for usage statistics
type UsageQuery struct {
	From     time.Time
	To       time.Time
	Bucket   time.Duration
	Location *time.Location
	Filters  UsageFilters
	Stack    string
}

// UsageLatency summarizes average and percentile latencies for a set of requests
type UsageLatency struct {
	AvgMS float64 `json:"avg_ms"`
	P50MS float64 `json:"p50_ms"`
	P95MS float64 `json:"p95_ms"`
	P99MS float64 `json:"p99_ms"`
}

// UsageStats summarizes outcomes, tokens, and duration; success includes tool calls, output limits, and stop
type UsageStats struct {
	Requests        int64        `json:"requests"`
	Succeeded       int64        `json:"succeeded"`
	Failed          int64        `json:"failed"`
	Canceled        int64        `json:"canceled"`
	RateLimited     int64        `json:"rate_limited"`
	InputTokens     int64        `json:"input_tokens"`
	ReasoningTokens int64        `json:"reasoning_tokens"`
	ReplyTokens     int64        `json:"reply_tokens"`
	TotalTokens     int64        `json:"total_tokens"`
	Duration        UsageLatency `json:"duration"`
	FirstEvent      UsageLatency `json:"first_event"`
	QueueAvgMS      float64      `json:"queue_avg_ms"`
	LastAt          *time.Time   `json:"last_at,omitempty"`
}

// UsageBucket stores rollup summary for a single time bucket
type UsageBucket struct {
	At time.Time `json:"at"`
	UsageStats
}

// UsageGroup stores rollup summary for a single dimension value
type UsageGroup struct {
	Key string `json:"key"`
	UsageStats
}

// UsagePair stores rollup summary for an account and model pair
type UsagePair struct {
	Account string `json:"account"`
	Model   string `json:"model"`
	UsageStats
}

// UsageSeries represents bucketed requests and tokens for a dimension value in stacked trends, Other merging lower ranks
type UsageSeries struct {
	Key      string  `json:"key"`
	Other    bool    `json:"other,omitempty"`
	Requests []int64 `json:"requests"`
	Tokens   []int64 `json:"tokens"`
}

// UsageRecent summarizes requests and tokens in recent minutes to calculate current throughput
type UsageRecent struct {
	Minutes  int   `json:"minutes"`
	Requests int64 `json:"requests"`
	Tokens   int64 `json:"tokens"`
}

// UsageReport returns dashboard summaries, previous period, bucket trends, stacked series, and filter candidates
type UsageReport struct {
	From          time.Time               `json:"from"`
	To            time.Time               `json:"to"`
	BucketSeconds int64                   `json:"bucket_seconds"`
	GeneratedAt   time.Time               `json:"generated_at"`
	LatestAt      *time.Time              `json:"latest_at,omitempty"`
	Totals        UsageStats              `json:"totals"`
	Previous      UsageStats              `json:"previous"`
	Recent        UsageRecent             `json:"recent"`
	Buckets       []UsageBucket           `json:"buckets"`
	StackBy       string                  `json:"stack_by"`
	Series        []UsageSeries           `json:"series"`
	Groups        map[string][]UsageGroup `json:"groups"`
	Pairs         []UsagePair             `json:"pairs"`
	Options       map[string][]string     `json:"options"`
}

// RequestAttempt records an unsuccessful upstream attempt
type RequestAttempt struct {
	Account    string `json:"account"`
	Channel    string `json:"channel"`
	Error      string `json:"error"`
	DurationMS int64  `json:"duration_ms"`
}

// UsageRecordQuery specifies time range, filters, status code, keywords, and pagination cursor for request records
type UsageRecordQuery struct {
	From    time.Time
	To      time.Time
	Filters UsageFilters
	Status  int
	Search  string
	Before  *UsageCursor
	Limit   int
}

// UsageCursor is request record pagination cursor, next page starting from earlier records
type UsageCursor struct {
	Time time.Time
	ID   string
}

// EncodeUsageCursor encodes pagination cursor into URL-safe string
func EncodeUsageCursor(cursor UsageCursor) string {
	return base64.RawURLEncoding.EncodeToString([]byte(strconv.FormatInt(cursor.Time.UnixMilli(), 10) + "|" + cursor.ID))
}

// decodeUsageCursor decodes URL-safe pagination cursor string
func decodeUsageCursor(value string) (*UsageCursor, error) {
	raw, err := base64.RawURLEncoding.DecodeString(value)
	if err != nil {
		return nil, fmt.Errorf("invalid cursor")
	}
	at, id, found := strings.Cut(string(raw), "|")
	milliseconds, err := strconv.ParseInt(at, 10, 64)
	if !found || err != nil || id == "" {
		return nil, fmt.Errorf("invalid cursor")
	}
	return &UsageCursor{Time: time.UnixMilli(milliseconds).UTC(), ID: id}, nil
}

// UsageRecord represents a ledger record for a completed request
type UsageRecord struct {
	ID              string           `json:"id"`
	Time            time.Time        `json:"time"`
	Protocol        string           `json:"protocol"`
	Path            string           `json:"path"`
	Model           string           `json:"model"`
	Account         string           `json:"account"`
	Channel         string           `json:"channel"`
	Status          int              `json:"status"`
	State           string           `json:"state"`
	DurationMS      int64            `json:"duration_ms"`
	FirstEventMS    int64            `json:"first_event_ms"`
	QueueMS         int64            `json:"queue_ms"`
	InputTokens     int64            `json:"input_tokens"`
	ReasoningTokens int64            `json:"reasoning_tokens"`
	ReplyTokens     int64            `json:"reply_tokens"`
	TotalTokens     int64            `json:"total_tokens"`
	ToolCalls       int              `json:"tool_calls"`
	Error           string           `json:"error"`
	Attempts        []RequestAttempt `json:"attempts"`
	HasBody         bool             `json:"has_body"`
}

// UsageRecordPage is a paginated page of request records in reverse completion order, NextCursor empty when done
type UsageRecordPage struct {
	Items      []UsageRecord `json:"items"`
	NextCursor string        `json:"next_cursor,omitempty"`
}

// RequestBody stores truncated request and response bodies for a request
type RequestBody struct {
	ID           string    `json:"id"`
	Time         time.Time `json:"time"`
	Request      string    `json:"request"`
	RequestSize  int64     `json:"request_size"`
	Response     string    `json:"response"`
	ResponseSize int64     `json:"response_size"`
}

// registerLedger registers routes for usage statistics, request records, and captured bodies
func (s *server) registerLedger(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/usage", s.handleUsage)
	mux.HandleFunc("GET /api/usage/records", s.handleUsageRecords)
	mux.HandleFunc("GET /api/usage/records.csv", s.handleUsageExport)
	mux.HandleFunc("GET /api/requests/{id}/body", s.handleRequestBody)
}

// usageBucketSizes defines bucket durations permitted for manual selection
var usageBucketSizes = []time.Duration{time.Minute, 5 * time.Minute, 15 * time.Minute, time.Hour, 24 * time.Hour}

// autoUsageBucket selects bucket duration according to time range length
func autoUsageBucket(span time.Duration) time.Duration {
	switch {
	case span <= 2*time.Hour:
		return time.Minute
	case span <= 12*time.Hour:
		return 5 * time.Minute
	case span <= 8*24*time.Hour:
		return time.Hour
	}
	return 24 * time.Hour
}

// parseUsageRange reads from and to timestamps, validating ordering and maximum window
func parseUsageRange(values map[string][]string) (time.Time, time.Time, error) {
	get := func(name string) string {
		if list := values[name]; len(list) > 0 {
			return list[0]
		}
		return ""
	}
	from, err := time.Parse(time.RFC3339, get("from"))
	if err != nil {
		return time.Time{}, time.Time{}, fmt.Errorf("from must be an RFC 3339 timestamp")
	}
	to, err := time.Parse(time.RFC3339, get("to"))
	if err != nil {
		return time.Time{}, time.Time{}, fmt.Errorf("to must be an RFC 3339 timestamp")
	}
	if !from.Before(to) {
		return time.Time{}, time.Time{}, fmt.Errorf("from must be earlier than to")
	}
	if to.Sub(from) > usageMaxSpan {
		return time.Time{}, time.Time{}, fmt.Errorf("time range cannot exceed 93 days")
	}
	return from.UTC(), to.UTC(), nil
}

// parseUsageFilters parses dimension filter values; comma-separated, ignoring empty and duplicate entries
func parseUsageFilters(values map[string][]string) UsageFilters {
	filters := UsageFilters{}
	for _, dimension := range UsageDimensions {
		for _, value := range values[dimension] {
			for _, item := range strings.Split(value, ",") {
				if item = strings.TrimSpace(item); item != "" && !slices.Contains(filters[dimension], item) {
					filters[dimension] = append(filters[dimension], item)
				}
			}
		}
	}
	return filters
}

func (s *server) handleUsage(w http.ResponseWriter, r *http.Request) {
	values := r.URL.Query()
	from, to, err := parseUsageRange(values)
	if err != nil {
		writeAdminError(w, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	query := UsageQuery{From: from, To: to, Location: time.UTC, Filters: parseUsageFilters(values), Stack: "model"}
	if name := values.Get("tz"); name != "" {
		if query.Location, err = time.LoadLocation(name); err != nil {
			writeAdminError(w, http.StatusBadRequest, "invalid_request", "tz must be an IANA timezone name")
			return
		}
	}
	query.Bucket = autoUsageBucket(to.Sub(from))
	if value := values.Get("bucket"); value != "" && value != "auto" {
		seconds, err := strconv.Atoi(value)
		query.Bucket = time.Duration(seconds) * time.Second
		if err != nil || !slices.Contains(usageBucketSizes, query.Bucket) {
			writeAdminError(w, http.StatusBadRequest, "invalid_request", "bucket must be auto, 60, 300, 900, 3600, or 86400")
			return
		}
	}
	if to.Sub(from)/query.Bucket > usageMaxBuckets {
		writeAdminError(w, http.StatusBadRequest, "invalid_request", fmt.Sprintf("bucket count exceeds %d; choose a larger granularity", usageMaxBuckets))
		return
	}
	if value := values.Get("stack"); value != "" {
		if !slices.Contains(UsageDimensions, value) {
			writeAdminError(w, http.StatusBadRequest, "invalid_request", "stack must be model, account, channel, protocol, or state")
			return
		}
		query.Stack = value
	}
	report, err := s.config.Ledger.Usage(r.Context(), query)
	if err != nil {
		writeAdminError(w, statusFromError(err), "usage_failed", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, report)
}

// parseUsageRecordQuery parses query parameters for request records range, filters, status, keywords, and cursor
func parseUsageRecordQuery(r *http.Request) (UsageRecordQuery, error) {
	values := r.URL.Query()
	from, to, err := parseUsageRange(values)
	if err != nil {
		return UsageRecordQuery{}, err
	}
	query := UsageRecordQuery{
		From: from, To: to, Filters: parseUsageFilters(values),
		Search: strings.TrimSpace(values.Get("q")), Limit: 50,
	}
	if value := values.Get("cursor"); value != "" {
		if query.Before, err = decodeUsageCursor(value); err != nil {
			return UsageRecordQuery{}, err
		}
	}
	if value := values.Get("status"); value != "" {
		if query.Status, err = strconv.Atoi(value); err != nil || query.Status < 100 || query.Status > 599 {
			return UsageRecordQuery{}, fmt.Errorf("status must be an HTTP status code")
		}
	}
	if value := values.Get("limit"); value != "" {
		if query.Limit, err = strconv.Atoi(value); err != nil || query.Limit < 1 || query.Limit > 200 {
			return UsageRecordQuery{}, fmt.Errorf("limit must be between 1 and 200")
		}
	}
	return query, nil
}

func (s *server) handleUsageRecords(w http.ResponseWriter, r *http.Request) {
	query, err := parseUsageRecordQuery(r)
	if err != nil {
		writeAdminError(w, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	page, err := s.config.Ledger.Records(r.Context(), query)
	if err != nil {
		writeAdminError(w, statusFromError(err), "usage_records_failed", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, page)
}

func (s *server) handleUsageExport(w http.ResponseWriter, r *http.Request) {
	query, err := parseUsageRecordQuery(r)
	if err != nil {
		writeAdminError(w, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	name := fmt.Sprintf("usage-%s-%s.csv", query.From.Format("20060102T150405Z"), query.To.Format("20060102T150405Z"))
	w.Header().Set("Content-Type", "text/csv; charset=utf-8")
	w.Header().Set("Content-Disposition", `attachment; filename="`+name+`"`)
	w.Header().Set("Cache-Control", "no-store")
	if err := s.config.Ledger.ExportRecords(r.Context(), query, w); err != nil {
		SetAccessLogError(r.Context(), err)
	}
}

func (s *server) handleRequestBody(w http.ResponseWriter, r *http.Request) {
	body, err := s.config.Ledger.RequestBody(r.Context(), r.PathValue("id"))
	if errors.Is(err, ErrRequestBodyNotFound) {
		writeAdminError(w, http.StatusNotFound, "request_body_not_found", err.Error())
		return
	}
	if err != nil {
		writeAdminError(w, http.StatusInternalServerError, "request_body_failed", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, body)
}

// bodyCapture stores truncated body and original byte length
type bodyCapture struct {
	mu   sync.Mutex
	data []byte
	size int64
}

func (capture *bodyCapture) add(data []byte) {
	capture.mu.Lock()
	capture.size += int64(len(data))
	if room := requestBodyLimit - len(capture.data); room > 0 {
		capture.data = append(capture.data, data[:min(room, len(data))]...)
	}
	capture.mu.Unlock()
}

func (capture *bodyCapture) result() (string, int64) {
	capture.mu.Lock()
	defer capture.mu.Unlock()
	return string(capture.data), capture.size
}

// captureReader buffers body prefix while reading request body
type captureReader struct {
	io.ReadCloser
	capture *bodyCapture
}

func (reader *captureReader) Read(data []byte) (int, error) {
	count, err := reader.ReadCloser.Read(data)
	reader.capture.add(data[:count])
	return count, err
}

// captureWriter buffers body prefix while writing response body
type captureWriter struct {
	http.ResponseWriter
	capture *bodyCapture
}

func (writer *captureWriter) Write(data []byte) (int, error) {
	count, err := writer.ResponseWriter.Write(data)
	writer.capture.add(data[:count])
	return count, err
}

func (writer *captureWriter) Unwrap() http.ResponseWriter {
	return writer.ResponseWriter
}

func (writer *captureWriter) setError(message string) {
	setAccessLogResponseError(writer.ResponseWriter, message)
}

// requestBodyMiddleware captures truncated POST request and response bodies when body capture is enabled
func requestBodyMiddleware(ledger RequestLedger, next http.Handler) http.Handler {
	if ledger == nil {
		return next
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || !ledger.BodyCapture() {
			next.ServeHTTP(w, r)
			return
		}
		request, response := &bodyCapture{}, &bodyCapture{}
		r.Body = &captureReader{ReadCloser: r.Body, capture: request}
		next.ServeHTTP(&captureWriter{ResponseWriter: w, capture: response}, r)
		metadata, ok := r.Context().Value(accessLogContextKey{}).(*accessLogMetadata)
		if !ok {
			return
		}
		body := RequestBody{ID: metadata.id(), Time: time.Now().UTC()}
		body.Request, body.RequestSize = request.result()
		body.Response, body.ResponseSize = response.result()
		ledger.SaveBody(body)
	})
}
