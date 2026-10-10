// Package requestdb stores request usage, hourly/daily summaries, and optional bodies in local SQLite
package requestdb

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"time"

	"github.com/Mag1cFall/AIStudio2API/internal/api"
	_ "modernc.org/sqlite"
)

// usageRetention is retention duration for usage records and summaries
const usageRetention = 90 * 24 * time.Hour

// bodyRetention is maximum number of recent captured bodies retained
const bodyRetention = 1000

// batchLimit is maximum records merged into a single write transaction
const batchLimit = 256

// queueLimit is maximum queue capacity for pending writes; excess records are dropped
const queueLimit = 4096

// hourMS is milliseconds in one hour
const hourMS = int64(time.Hour / time.Millisecond)

// rollupColumns defines columns in hourly and daily rollup tables; start is period begin
const rollupColumns = `
 start INTEGER NOT NULL, model TEXT NOT NULL, account TEXT NOT NULL, channel TEXT NOT NULL, protocol TEXT NOT NULL,
 state TEXT NOT NULL, status INTEGER NOT NULL, requests INTEGER NOT NULL,
 input_tokens INTEGER NOT NULL, reasoning_tokens INTEGER NOT NULL, reply_tokens INTEGER NOT NULL, total_tokens INTEGER NOT NULL,
 duration_ms INTEGER NOT NULL, first_event_ms INTEGER NOT NULL, first_events INTEGER NOT NULL, queue_ms INTEGER NOT NULL,
 duration_hist BLOB NOT NULL, first_event_hist BLOB NOT NULL, last_time INTEGER NOT NULL,
 PRIMARY KEY (start, model, account, channel, protocol, state, status)`

const schema = `
CREATE TABLE IF NOT EXISTS requests (
 id TEXT PRIMARY KEY, time INTEGER NOT NULL, protocol TEXT NOT NULL, path TEXT NOT NULL,
 model TEXT NOT NULL, account TEXT NOT NULL, channel TEXT NOT NULL, status INTEGER NOT NULL, state TEXT NOT NULL,
 duration_ms INTEGER NOT NULL, first_event_ms INTEGER NOT NULL, queue_ms INTEGER NOT NULL,
 input_tokens INTEGER NOT NULL, reasoning_tokens INTEGER NOT NULL, reply_tokens INTEGER NOT NULL,
 total_tokens INTEGER NOT NULL, tool_calls INTEGER NOT NULL, error TEXT NOT NULL, attempts TEXT NOT NULL);
CREATE INDEX IF NOT EXISTS requests_time ON requests(time);
CREATE INDEX IF NOT EXISTS requests_model_time ON requests(model, time);
CREATE INDEX IF NOT EXISTS requests_account_time ON requests(account, time);
CREATE INDEX IF NOT EXISTS requests_state_time ON requests(state, time);
CREATE TABLE IF NOT EXISTS hours (` + rollupColumns + `) WITHOUT ROWID;
CREATE TABLE IF NOT EXISTS days (` + rollupColumns + `) WITHOUT ROWID;
CREATE TABLE IF NOT EXISTS bodies (
 seq INTEGER PRIMARY KEY AUTOINCREMENT, id TEXT NOT NULL UNIQUE, time INTEGER NOT NULL,
 request TEXT NOT NULL, request_size INTEGER NOT NULL, response TEXT NOT NULL, response_size INTEGER NOT NULL);`

// Row is a usage record for a completed request
type Row struct {
	ID              string
	Time            time.Time
	Protocol        string
	Path            string
	Model           string
	Account         string
	Channel         string
	Status          int
	State           string
	Duration        time.Duration
	FirstEvent      time.Duration
	Queue           time.Duration
	InputTokens     int64
	ReasoningTokens int64
	ReplyTokens     int64
	TotalTokens     int64
	ToolCalls       int
	Error           string
	Attempts        []api.RequestAttempt
}

// write is a usage row or body handled by the writer goroutine
type write struct {
	row  *Row
	body *api.RequestBody
}

// Logger outputs operational logs for ledger write failures and dropped records
type Logger func(level string, message string)

// Store writes request usage and rollups in serial batches and provides query APIs
type Store struct {
	db      *sql.DB
	writes  chan write
	done    chan struct{}
	log     Logger
	capture atomic.Bool
	dropped atomic.Int64
	mu      sync.RWMutex
	closed  bool
}

// Open opens or initializes ledger database and launches writer goroutine
func Open(path string, log Logger) (*Store, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, err
	}
	options := url.Values{"_pragma": {"busy_timeout(10000)", "journal_mode(WAL)", "synchronous(NORMAL)"}}
	db, err := sql.Open("sqlite", "file:"+filepath.ToSlash(path)+"?"+options.Encode())
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(4)
	if _, err := db.Exec(schema); err != nil {
		return nil, errors.Join(fmt.Errorf("initialize request ledger: %w", err), db.Close())
	}
	store := &Store{db: db, writes: make(chan write, queueLimit), done: make(chan struct{}), log: log}
	if err := store.alignDays(); err != nil {
		return nil, errors.Join(fmt.Errorf("rebuild daily rollups: %w", err), db.Close())
	}
	go store.run()
	return store, nil
}

// Close writes all queued records before closing database
func (store *Store) Close() error {
	if store == nil {
		return nil
	}
	store.mu.Lock()
	if store.closed {
		store.mu.Unlock()
		return nil
	}
	store.closed = true
	close(store.writes)
	store.mu.Unlock()
	<-store.done
	return store.db.Close()
}

// SetBodyCapture sets whether to capture request and response bodies
func (store *Store) SetBodyCapture(enabled bool) {
	if store != nil {
		store.capture.Store(enabled)
	}
}

// BodyCapture returns whether request and response bodies are captured
func (store *Store) BodyCapture() bool {
	return store != nil && store.capture.Load()
}

// Record queues a usage record for writing without waiting for disk sync
func (store *Store) Record(row Row) {
	store.enqueue(write{row: &row})
}

// SaveBody queues a truncated body for writing without waiting for disk sync
func (store *Store) SaveBody(body api.RequestBody) {
	if body.ID != "" {
		store.enqueue(write{body: &body})
	}
}

// enqueue hands record to writer goroutine, counting drops when queue is full without blocking
func (store *Store) enqueue(item write) {
	if store == nil {
		return
	}
	store.mu.RLock()
	defer store.mu.RUnlock()
	if store.closed {
		return
	}
	select {
	case store.writes <- item:
	default:
		store.dropped.Add(1)
	}
}

// run merges consecutively arriving records into transactions and prunes daily
func (store *Store) run() {
	defer close(store.done)
	store.prune()
	prune := time.NewTicker(24 * time.Hour)
	defer prune.Stop()
	for {
		select {
		case item, ok := <-store.writes:
			if !ok {
				return
			}
			batch := []write{item}
			for len(batch) < batchLimit {
				select {
				case next, ok := <-store.writes:
					if !ok {
						store.commit(batch)
						return
					}
					batch = append(batch, next)
					continue
				default:
				}
				break
			}
			store.commit(batch)
		case <-prune.C:
			store.prune()
		}
	}
}

// commit writes batch of records, logging write failures and drops
func (store *Store) commit(batch []write) {
	if err := store.write(batch); err != nil {
		store.log("ERROR", fmt.Sprintf("request ledger write failed | records=%d | error=%v", len(batch), err))
	}
	if dropped := store.dropped.Swap(0); dropped > 0 {
		store.log("WARN", fmt.Sprintf("request ledger write queue full | dropped=%d", dropped))
	}
}

// rollupKey is primary key for rollup tables
type rollupKey struct {
	start    int64
	model    string
	account  string
	channel  string
	protocol string
	state    string
	status   int
}

// keyOf returns rollup key for entry at given period start
func keyOf(start int64, value *entry) rollupKey {
	return rollupKey{
		start: start, model: value.model, account: value.account, channel: value.channel,
		protocol: value.protocol, state: value.state, status: value.status,
	}
}

// collect accumulates entry into summary with matching key, keeping copy on first appearance
func collect(target map[rollupKey]*entry, key rollupKey, value *entry) {
	if current := target[key]; current != nil {
		current.combine(value)
		return
	}
	copied := value.clone()
	copied.time = key.start
	target[key] = copied
}

// write inserts records, updates hourly/daily rollups, and prunes body counts in one transaction
func (store *Store) write(batch []write) (err error) {
	tx, err := store.db.Begin()
	if err != nil {
		return err
	}
	defer func() {
		if err != nil {
			err = errors.Join(err, tx.Rollback())
		}
	}()
	hours, days := map[rollupKey]*entry{}, map[rollupKey]*entry{}
	bodies := false
	for _, item := range batch {
		if row := item.row; row != nil {
			attempts := row.Attempts
			if attempts == nil {
				attempts = []api.RequestAttempt{}
			}
			encoded, err := json.Marshal(attempts)
			if err != nil {
				return err
			}
			result, err := tx.Exec(`INSERT OR IGNORE INTO requests VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
				row.ID, row.Time.UnixMilli(), row.Protocol, row.Path, row.Model, row.Account, row.Channel, row.Status, row.State,
				row.Duration.Milliseconds(), row.FirstEvent.Milliseconds(), row.Queue.Milliseconds(),
				row.InputTokens, row.ReasoningTokens, row.ReplyTokens, row.TotalTokens, row.ToolCalls, row.Error, string(encoded))
			if err != nil {
				return err
			}
			if inserted, err := result.RowsAffected(); err != nil {
				return err
			} else if inserted == 1 {
				value := rowEntry(row)
				day, _ := localDay(value.time)
				collect(hours, keyOf(floorDiv(value.time, hourMS)*hourMS, value), value)
				collect(days, keyOf(day, value), value)
			}
		}
		if body := item.body; body != nil {
			bodies = true
			if _, err := tx.Exec(`INSERT OR REPLACE INTO bodies (id,time,request,request_size,response,response_size) VALUES (?,?,?,?,?,?)`,
				body.ID, body.Time.UnixMilli(), body.Request, body.RequestSize, body.Response, body.ResponseSize); err != nil {
				return err
			}
		}
	}
	for key, value := range hours {
		if err := mergeRollup(tx, "hours", key, value); err != nil {
			return err
		}
	}
	for key, value := range days {
		if err := mergeRollup(tx, "days", key, value); err != nil {
			return err
		}
	}
	if bodies {
		if _, err := tx.Exec(`DELETE FROM bodies WHERE seq <= (SELECT MAX(seq) FROM bodies) - ?`, bodyRetention); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// mergeRollup accumulates current batch into existing rollups with same key
func mergeRollup(tx *sql.Tx, table string, key rollupKey, value *entry) error {
	existing := &entry{}
	var durations, firstEvents []byte
	err := tx.QueryRow(`SELECT requests, input_tokens, reasoning_tokens, reply_tokens, total_tokens, duration_ms, first_event_ms,
 first_events, queue_ms, duration_hist, first_event_hist, last_time FROM `+table+`
 WHERE start=? AND model=? AND account=? AND channel=? AND protocol=? AND state=? AND status=?`,
		key.start, key.model, key.account, key.channel, key.protocol, key.state, key.status).Scan(
		&existing.requests, &existing.inputTokens, &existing.reasoningTokens, &existing.replyTokens, &existing.totalTokens,
		&existing.durationMS, &existing.firstEventMS, &existing.firstEvents, &existing.queueMS, &durations, &firstEvents, &existing.lastTime)
	switch {
	case errors.Is(err, sql.ErrNoRows):
	case err != nil:
		return err
	default:
		if existing.duration, err = decodeCounts(durations); err != nil {
			return err
		}
		if existing.firstEvent, err = decodeCounts(firstEvents); err != nil {
			return err
		}
		value.combine(existing)
	}
	_, err = tx.Exec(`INSERT OR REPLACE INTO `+table+` VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		key.start, key.model, key.account, key.channel, key.protocol, key.state, key.status, value.requests,
		value.inputTokens, value.reasoningTokens, value.replyTokens, value.totalTokens,
		value.durationMS, value.firstEventMS, value.firstEvents, value.queueMS,
		value.duration.encode(), value.firstEvent.encode(), value.lastTime)
	return err
}

// prune deletes data older than retention period, aligning boundaries across raw and rollup layers
func (store *Store) prune() {
	day, _ := localDay(time.Now().Add(-usageRetention).UnixMilli())
	hour := floorDiv(day, hourMS) * hourMS
	_, requestsErr := store.db.Exec(`DELETE FROM requests WHERE time < ?`, hour)
	_, hoursErr := store.db.Exec(`DELETE FROM hours WHERE start < ?`, hour)
	_, daysErr := store.db.Exec(`DELETE FROM days WHERE start < ?`, day)
	if err := errors.Join(requestsErr, hoursErr, daysErr); err != nil {
		store.log("ERROR", fmt.Sprintf("request ledger pruning failed | error=%v", err))
	}
}

// alignDays rebuilds daily rollups from hourly rollups and raw records if server timezone shifted
func (store *Store) alignDays() (err error) {
	rows, err := store.db.Query(`SELECT start FROM days GROUP BY start`)
	if err != nil {
		return err
	}
	aligned := true
	for rows.Next() {
		var start int64
		if err := rows.Scan(&start); err != nil {
			return errors.Join(err, rows.Close())
		}
		if day, _ := localDay(start); day != start {
			aligned = false
		}
	}
	if err := errors.Join(rows.Err(), rows.Close()); err != nil || aligned {
		return err
	}
	var earliest sql.NullInt64
	if err := store.db.QueryRow(`SELECT MIN(start) FROM hours`).Scan(&earliest); err != nil {
		return err
	}
	tx, err := store.db.Begin()
	if err != nil {
		return err
	}
	defer func() {
		if err != nil {
			err = errors.Join(err, tx.Rollback())
		}
	}()
	if _, err := tx.Exec(`DELETE FROM days`); err != nil {
		return err
	}
	rebuilt, now := 0, time.Now().UnixMilli()
	for start, end := localDay(earliest.Int64); earliest.Valid && start <= now; start, end = localDay(end) {
		days := map[rollupKey]*entry{}
		for _, part := range planHours(start, end, nil) {
			if err := store.scan(context.Background(), part, nil, func(value *entry) { collect(days, keyOf(start, value), value) }); err != nil {
				return err
			}
		}
		for key, value := range days {
			if err := mergeRollup(tx, "days", key, value); err != nil {
				return err
			}
		}
		rebuilt++
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	store.log("INFO", fmt.Sprintf("request ledger rebuilt daily rollups for server timezone | days=%d", rebuilt))
	return nil
}

// RequestBody returns captured truncated body for request
func (store *Store) RequestBody(ctx context.Context, id string) (api.RequestBody, error) {
	body := api.RequestBody{ID: id}
	var at int64
	err := store.db.QueryRowContext(ctx, `SELECT time,request,request_size,response,response_size FROM bodies WHERE id=?`, id).
		Scan(&at, &body.Request, &body.RequestSize, &body.Response, &body.ResponseSize)
	if errors.Is(err, sql.ErrNoRows) {
		return api.RequestBody{}, api.ErrRequestBodyNotFound
	}
	if err != nil {
		return api.RequestBody{}, err
	}
	body.Time = time.UnixMilli(at).UTC()
	return body, nil
}

// floorDiv returns integer division rounded towards negative infinity
func floorDiv(value, divisor int64) int64 {
	quotient := value / divisor
	if value%divisor != 0 && (value < 0) != (divisor < 0) {
		quotient--
	}
	return quotient
}
