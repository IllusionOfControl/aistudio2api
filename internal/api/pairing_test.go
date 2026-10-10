package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// pairingAdmin records accounts uploaded via pairing
type pairingAdmin struct {
	AdminService
	imported []AccountStateInput
}

// ImportAccountState stores uploaded content and returns matching account
func (admin *pairingAdmin) ImportAccountState(_ context.Context, input AccountStateInput) (AdminAccount, error) {
	admin.imported = append(admin.imported, input)
	return AdminAccount{ID: input.Label, Label: input.Label}, nil
}

// TestPairingTokenAccount verifies tokens are issued by admin sessions, uploads do not require admin session, and new tokens invalidate previous
func TestPairingTokenAccount(t *testing.T) {
	admin := &pairingAdmin{}
	handler := NewHandler(nil, Config{Admin: admin, AdminAuthEnabled: true, AdminUsername: "admin", AdminPassword: "secret"})
	serve := func(method, path, body string, header http.Header) *httptest.ResponseRecorder {
		request := httptest.NewRequest(method, path, strings.NewReader(body))
		for key, values := range header {
			request.Header[key] = values
		}
		recorder := httptest.NewRecorder()
		handler.ServeHTTP(recorder, request)
		return recorder
	}
	if response := serve("POST", "/api/pairing", "", nil); response.Code != http.StatusUnauthorized {
		t.Fatalf("issued token without session = %d", response.Code)
	}
	login := serve("POST", "/api/auth/login", `{"username":"admin","password":"secret"}`, nil)
	cookies := login.Result().Cookies()
	if login.Code != http.StatusOK || len(cookies) == 0 {
		t.Fatalf("login = %d %s", login.Code, login.Body)
	}
	session := http.Header{"Cookie": {cookies[0].String()}}
	issue := func() string {
		response := serve("POST", "/api/pairing", "", session)
		var token PairingToken
		if response.Code != http.StatusCreated || json.Unmarshal(response.Body.Bytes(), &token) != nil || token.Token == "" {
			t.Fatalf("issued token = %d %s", response.Code, response.Body)
		}
		if remaining := time.Until(token.ExpiresAt); remaining <= 9*time.Minute || remaining > 10*time.Minute {
			t.Fatalf("token validity = %s", remaining)
		}
		return token.Token
	}
	upload := func(token string) int {
		header := http.Header{"Content-Type": {"application/json"}}
		if token != "" {
			header.Set("Authorization", "Bearer "+token)
		}
		body := `{"label":"a@example.com","locale":"en-US","timezone":"UTC","storage_state":{"cookies":[],"origins":[]},"fingerprint":{"config":{"a":1}}}`
		return serve("POST", "/api/pairing/accounts", body, header).Code
	}
	first := issue()
	if code := upload(""); code != http.StatusUnauthorized {
		t.Fatalf("upload without token = %d", code)
	}
	if code := upload(first + "x"); code != http.StatusUnauthorized {
		t.Fatalf("upload with invalid token = %d", code)
	}
	if code := upload(first); code != http.StatusCreated {
		t.Fatalf("upload with token = %d", code)
	}
	if code := upload(first); code != http.StatusCreated {
		t.Fatalf("second account within validity = %d", code)
	}
	second := issue()
	if code := upload(first); code != http.StatusUnauthorized {
		t.Fatalf("upload with old token = %d", code)
	}
	if code := upload(second); code != http.StatusCreated {
		t.Fatalf("upload with new token = %d", code)
	}
	if len(admin.imported) != 3 || admin.imported[0].Label != "a@example.com" || string(admin.imported[0].Fingerprint) != `{"config":{"a":1}}` {
		t.Fatalf("imported content = %+v", admin.imported)
	}
}

// TestPairingTokenExpiry verifies token expires after TTL
func TestPairingTokenExpiry(t *testing.T) {
	tokens := &pairingTokens{}
	now := time.Unix(1_700_000_000, 0)
	token := tokens.issue(now)
	if !tokens.valid(token.Token, now.Add(pairingTokenLifetime-time.Second)) {
		t.Fatal("token invalid before expiration")
	}
	if tokens.valid(token.Token, now.Add(pairingTokenLifetime)) {
		t.Fatal("token still valid after expiration")
	}
	if (&pairingTokens{}).valid("", now) {
		t.Fatal("empty token valid when unissued")
	}
}
