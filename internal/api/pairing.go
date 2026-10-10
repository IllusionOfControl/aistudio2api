package api

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/json"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/Mag1cFall/AIStudio2API/internal/aistudio"
)

// pairingTokenLifetime is validity duration for remote pairing tokens
const pairingTokenLifetime = 10 * time.Minute

// PairingToken represents remote pairing token and its expiration time
type PairingToken struct {
	Token     string    `json:"token"`
	ExpiresAt time.Time `json:"expires_at"`
}

// AccountStateInput represents account authentication state and Camoufox fingerprint uploaded via remote pairing
type AccountStateInput struct {
	Label        string                `json:"label"`
	Locale       string                `json:"locale"`
	Timezone     string                `json:"timezone"`
	StorageState aistudio.StorageState `json:"storage_state"`
	Fingerprint  json.RawMessage       `json:"fingerprint,omitempty"`
}

// pairingTokens stores digest of currently active pairing token
type pairingTokens struct {
	mu      sync.Mutex
	digest  [sha256.Size]byte
	expires time.Time
}

// issue generates a new pairing token, invalidating any previous token
func (tokens *pairingTokens) issue(now time.Time) PairingToken {
	token := rand.Text()
	tokens.mu.Lock()
	defer tokens.mu.Unlock()
	tokens.digest = sha256.Sum256([]byte(token))
	tokens.expires = now.Add(pairingTokenLifetime)
	return PairingToken{Token: token, ExpiresAt: tokens.expires}
}

// valid checks whether token is currently active and unexpired
func (tokens *pairingTokens) valid(token string, now time.Time) bool {
	digest := sha256.Sum256([]byte(token))
	tokens.mu.Lock()
	defer tokens.mu.Unlock()
	return now.Before(tokens.expires) && subtle.ConstantTimeCompare(digest[:], tokens.digest[:]) == 1
}

// handleCreatePairing creates a new remote pairing token
func (s *server) handleCreatePairing(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusCreated, s.pairing.issue(time.Now()))
}

// handlePairingAccount imports an account using pairing token without requiring management session
func (s *server) handlePairingAccount(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	token, _ := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
	if !s.pairing.valid(strings.TrimSpace(token), time.Now()) {
		writeAdminError(w, http.StatusUnauthorized, "pairing_token_invalid", "Pairing token is invalid or expired")
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
	var input AccountStateInput
	if err := decodeJSON(r, &input); err != nil {
		writeAdminError(w, http.StatusBadRequest, "invalid_request", "Invalid account state")
		return
	}
	account, err := s.config.Admin.ImportAccountState(r.Context(), input)
	if err != nil {
		writeAdminUpstreamError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, map[string]AdminAccount{"account": account})
}
