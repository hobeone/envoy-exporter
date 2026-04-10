package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"

	jwt "github.com/golang-jwt/jwt/v4"
	gateway "github.com/hobeone/enphase-gateway"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// makeTestJWT creates a signed JWT with the given expiry for use in tests.
func makeTestJWT(exp time.Time) string {
	claims := jwt.MapClaims{"exp": float64(exp.Unix())}
	token := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	signed, _ := token.SignedString([]byte("test-secret"))
	return signed
}

// TestAuthenticateWithEnphase tests cannot be parallelised: they write to the
// gateway package-level URL variables, which would race if tests ran concurrently.

func TestAuthenticateWithEnphase(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/login/login.json":
			if r.Method != http.MethodPost {
				http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
				return
			}
			if err := r.ParseForm(); err != nil {
				http.Error(w, "bad request", http.StatusBadRequest)
				return
			}
			if r.FormValue("user[email]") == "user@example.com" && r.FormValue("user[password]") == "pass" {
				w.Header().Set("Content-Type", "application/json")
				fmt.Fprint(w, `{"session_id":"test-session"}`)
				return
			}
			http.Error(w, "unauthorized", http.StatusUnauthorized)
		case "/tokens":
			if r.Method != http.MethodPost {
				http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
				return
			}
			w.WriteHeader(http.StatusOK)
			fmt.Fprint(w, "mock-jwt-token")
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	origEnlighten := gateway.EnlightenBaseURL
	origEntrez := gateway.EntrezBaseURL
	gateway.EnlightenBaseURL = server.URL
	gateway.EntrezBaseURL = server.URL
	defer func() {
		gateway.EnlightenBaseURL = origEnlighten
		gateway.EntrezBaseURL = origEntrez
	}()

	token, err := AuthenticateWithEnphase("user@example.com", "pass", "12345")
	require.NoError(t, err)
	assert.Equal(t, "mock-jwt-token", token)
}

func TestAuthenticateWithEnphase_LoginFails(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/login/login.json" {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
		}
	}))
	defer server.Close()

	origEnlighten := gateway.EnlightenBaseURL
	origEntrez := gateway.EntrezBaseURL
	gateway.EnlightenBaseURL = server.URL
	gateway.EntrezBaseURL = server.URL
	defer func() {
		gateway.EnlightenBaseURL = origEnlighten
		gateway.EntrezBaseURL = origEntrez
	}()

	_, err := AuthenticateWithEnphase("bad", "creds", "12345")
	assert.Error(t, err)
}

func TestAuthenticateWithEnphase_JSONResponse(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/login/login.json":
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprint(w, `{"session_id":"test-session"}`)
		case "/tokens":
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprint(w, `{"generation_time":1000000,"token":"jwt-from-json","expires_at":9999999}`)
		}
	}))
	defer server.Close()

	origEnlighten := gateway.EnlightenBaseURL
	origEntrez := gateway.EntrezBaseURL
	gateway.EnlightenBaseURL = server.URL
	gateway.EntrezBaseURL = server.URL
	defer func() {
		gateway.EnlightenBaseURL = origEnlighten
		gateway.EntrezBaseURL = origEntrez
	}()

	token, err := AuthenticateWithEnphase("user", "pass", "serial")
	require.NoError(t, err)
	assert.Equal(t, "jwt-from-json", token)
}

func TestParseJWTExpiry(t *testing.T) {
	t.Parallel()

	future := time.Now().Add(24 * time.Hour).Truncate(time.Second)
	raw := makeTestJWT(future)

	expiry, err := parseJWTExpiry(raw)
	require.NoError(t, err)
	assert.Equal(t, future, expiry)
}

func TestParseJWTExpiry_InvalidToken(t *testing.T) {
	t.Parallel()

	_, err := parseJWTExpiry("not-a-jwt")
	assert.Error(t, err)
}

func TestFetchWithRetry_ImmediateSuccess(t *testing.T) {
	t.Parallel()

	cfg := &Config{Username: "u", Password: "p", SerialNumber: "s"}
	fetch := func(_, _, _ string) (string, error) { return "token", nil }

	token, err := fetchWithRetry(context.Background(), cfg, time.Millisecond, fetch)
	require.NoError(t, err)
	assert.Equal(t, "token", token)
}

func TestFetchWithRetry_RetriesOnError(t *testing.T) {
	t.Parallel()

	cfg := &Config{Username: "u", Password: "p", SerialNumber: "s"}
	calls := 0
	fetch := func(_, _, _ string) (string, error) {
		calls++
		if calls < 3 {
			return "", errors.New("transient")
		}
		return "token", nil
	}

	token, err := fetchWithRetry(context.Background(), cfg, time.Millisecond, fetch)
	require.NoError(t, err)
	assert.Equal(t, "token", token)
	assert.Equal(t, 3, calls)
}

func TestFetchWithRetry_CancelledContext(t *testing.T) {
	t.Parallel()

	cfg := &Config{Username: "u", Password: "p", SerialNumber: "s"}
	fetch := func(_, _, _ string) (string, error) { return "", errors.New("always fails") }

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()

	_, err := fetchWithRetry(ctx, cfg, 10*time.Millisecond, fetch)
	assert.Error(t, err)
}

func TestJWTRefresher_RefreshesExpiredToken(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	// Expiry in the past → delay will be 0, refresh fires immediately.
	expiry := time.Now().Add(-1 * time.Hour)
	cfg := &Config{
		Username:      "user",
		Password:      "pass",
		SerialNumber:  "12345",
		RetryInterval: 1,
	}

	newJWT := makeTestJWT(time.Now().Add(24 * time.Hour))
	fetchCalled := make(chan struct{}, 1)
	mockFetch := func(username, password, serial string) (string, error) {
		fetchCalled <- struct{}{}
		return newJWT, nil
	}

	reconnectCh := make(chan struct{}, 1)
	go jwtRefresher(ctx, cfg, expiry, reconnectCh, mockFetch, nil)

	select {
	case <-fetchCalled:
	case <-time.After(time.Second):
		t.Fatal("JWT refresh was not called")
	}

	select {
	case <-reconnectCh:
	case <-time.After(time.Second):
		t.Fatal("reconnect signal not sent")
	}

	assert.Equal(t, newJWT, cfg.JWT)
}

func TestJWTRefresher_RetriesOnFetchError(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Second)
	defer cancel()

	expiry := time.Now().Add(-1 * time.Hour)
	cfg := &Config{
		Username:      "user",
		Password:      "pass",
		SerialNumber:  "12345",
		RetryInterval: 1,
	}

	attempts := 0
	newJWT := makeTestJWT(time.Now().Add(24 * time.Hour))
	mockFetch := func(username, password, serial string) (string, error) {
		attempts++
		if attempts < 2 {
			return "", fmt.Errorf("transient error")
		}
		return newJWT, nil
	}

	reconnectCh := make(chan struct{}, 1)
	go jwtRefresher(ctx, cfg, expiry, reconnectCh, mockFetch, nil)

	select {
	case <-reconnectCh:
		assert.GreaterOrEqual(t, attempts, 2)
	case <-ctx.Done():
		t.Fatal("JWT refresh did not succeed within timeout")
	}
}

// TestJWTRefresher_ContextCancelledDuringDelay verifies that jwtRefresher exits
// promptly when the context is cancelled while it is sleeping before the next
// refresh (i.e. the token is not yet near expiry).
func TestJWTRefresher_ContextCancelledDuringDelay(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(context.Background())

	// Expiry far in the future → lead-time delay will be ~22 hours.
	expiry := time.Now().Add(24 * time.Hour)
	cfg := &Config{
		Username:     "user",
		Password:     "pass",
		SerialNumber: "12345",
		// JWTRefreshLeadTime=0 defaults to 60 minutes, so delay ≈ 23 hours.
	}

	fetchCalled := false
	mockFetch := func(_, _, _ string) (string, error) {
		fetchCalled = true
		return "", errors.New("should not be called")
	}

	done := make(chan struct{})
	go func() {
		jwtRefresher(ctx, cfg, expiry, make(chan struct{}, 1), mockFetch, nil)
		close(done)
	}()

	cancel() // cancel immediately, before the multi-hour delay fires

	select {
	case <-done:
		assert.False(t, fetchCalled, "fetch should not have been called before expiry")
	case <-time.After(time.Second):
		t.Fatal("jwtRefresher did not exit after context cancellation")
	}
}

func TestJWTRefresher_PersistsCalled(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	expiry := time.Now().Add(-1 * time.Hour)
	cfg := &Config{
		Username:      "user",
		Password:      "pass",
		SerialNumber:  "12345",
		RetryInterval: 1,
	}

	newJWT := makeTestJWT(time.Now().Add(24 * time.Hour))
	mockFetch := func(_, _, _ string) (string, error) { return newJWT, nil }

	var persistedToken string
	mockPersist := func(token string) error {
		persistedToken = token
		return nil
	}

	reconnectCh := make(chan struct{}, 1)
	go jwtRefresher(ctx, cfg, expiry, reconnectCh, mockFetch, mockPersist)

	select {
	case <-reconnectCh:
	case <-time.After(time.Second):
		t.Fatal("reconnect signal not sent")
	}

	assert.Equal(t, newJWT, persistedToken)
}

func TestPersistJWTToConfig_UpdatesExistingField(t *testing.T) {
	t.Parallel()

	content := []byte(`# Envoy config
address: https://192.168.1.100
serial: "12345"
jwt: old-token
username: user@example.com
`)
	f, err := os.CreateTemp("", "config-*.yaml")
	require.NoError(t, err)
	defer os.Remove(f.Name())
	_, err = f.Write(content)
	require.NoError(t, err)
	require.NoError(t, f.Close())

	require.NoError(t, persistJWTToConfig(f.Name(), "new-token"))

	cfg, err := LoadConfig(f.Name())
	require.NoError(t, err)
	assert.Equal(t, "new-token", cfg.JWT)

	// Other fields must be preserved.
	assert.Equal(t, "https://192.168.1.100", cfg.Address)
	assert.Equal(t, "12345", cfg.SerialNumber)
	assert.Equal(t, "user@example.com", cfg.Username)
}

func TestPersistJWTToConfig_AddsAbsentField(t *testing.T) {
	t.Parallel()

	// Config with no jwt field — should be added.
	content := []byte(`address: https://192.168.1.100
serial: "12345"
username: user@example.com
password: secret
influxdb: http://influxdb:8086
influxdb_token: tok
influxdb_org: org
influxdb_bucket: bucket
`)
	f, err := os.CreateTemp("", "config-*.yaml")
	require.NoError(t, err)
	defer os.Remove(f.Name())
	_, err = f.Write(content)
	require.NoError(t, err)
	require.NoError(t, f.Close())

	require.NoError(t, persistJWTToConfig(f.Name(), "brand-new-token"))

	cfg, err := LoadConfig(f.Name())
	require.NoError(t, err)
	assert.Equal(t, "brand-new-token", cfg.JWT)
	assert.Equal(t, "https://192.168.1.100", cfg.Address)
}

func TestPersistJWTToConfig_FileNotFound(t *testing.T) {
	t.Parallel()

	err := persistJWTToConfig("/nonexistent/path/config.yaml", "token")
	assert.Error(t, err)
}

func TestParseLogLevel(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		input     string
		wantLevel slog.Level
		wantErr   bool
	}{
		{name: "empty string defaults to info", input: "", wantLevel: slog.LevelInfo},
		{name: "info lowercase", input: "info", wantLevel: slog.LevelInfo},
		{name: "info uppercase", input: "INFO", wantLevel: slog.LevelInfo},
		{name: "debug lowercase", input: "debug", wantLevel: slog.LevelDebug},
		{name: "debug uppercase", input: "DEBUG", wantLevel: slog.LevelDebug},
		{name: "warn lowercase", input: "warn", wantLevel: slog.LevelWarn},
		{name: "warning alias", input: "warning", wantLevel: slog.LevelWarn},
		{name: "error lowercase", input: "error", wantLevel: slog.LevelError},
		{name: "error uppercase", input: "ERROR", wantLevel: slog.LevelError},
		{name: "whitespace trimmed", input: "  debug  ", wantLevel: slog.LevelDebug},
		{name: "verbose is unknown", input: "verbose", wantErr: true},
		{name: "trace is unknown", input: "trace", wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got, err := parseLogLevel(tt.input)
			if tt.wantErr {
				assert.Error(t, err)
			} else {
				require.NoError(t, err)
				assert.Equal(t, tt.wantLevel, got)
			}
		})
	}
}

func TestRun_FlagError(t *testing.T) {
	err := run([]string{"-nonexistent-flag"})
	assert.Error(t, err)
}

func TestRun_MissingConfig(t *testing.T) {
	t.Parallel()

	err := run([]string{"-config", "/nonexistent/path.yaml"})
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "failed to load config")
}

func TestRun_ValidationFail(t *testing.T) {
	t.Parallel()

	content := []byte("address: https://192.168.1.1\nserial: 12345\n")
	f, err := os.CreateTemp("", "cfg-*.yaml")
	require.NoError(t, err)
	defer os.Remove(f.Name())
	_, err = f.Write(content)
	require.NoError(t, err)
	require.NoError(t, f.Close())

	err = run([]string{"-config", f.Name()})
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "configuration validation failed")
}
