package main

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"

	jwt "github.com/golang-jwt/jwt/v4"
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

func TestAuthenticateWithEnphase(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/login/login":
			if r.Method != http.MethodPost {
				http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
				return
			}
			if err := r.ParseForm(); err != nil {
				http.Error(w, "bad request", http.StatusBadRequest)
				return
			}
			if r.FormValue("user[email]") == "user@example.com" && r.FormValue("user[password]") == "pass" {
				w.WriteHeader(http.StatusOK)
				return
			}
			http.Error(w, "unauthorized", http.StatusUnauthorized)
		case "/entrez-auth-token":
			if r.URL.Query().Get("serial_num") == "12345" {
				w.WriteHeader(http.StatusOK)
				fmt.Fprint(w, "mock-jwt-token")
				return
			}
			http.Error(w, "bad request", http.StatusBadRequest)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	origURL := EnphaseBaseURL
	EnphaseBaseURL = server.URL
	defer func() { EnphaseBaseURL = origURL }()

	token, err := AuthenticateWithEnphase("user@example.com", "pass", "12345")
	require.NoError(t, err)
	assert.Equal(t, "mock-jwt-token", token)
}

func TestAuthenticateWithEnphase_LoginFails(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/login/login" {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
		}
	}))
	defer server.Close()

	origURL := EnphaseBaseURL
	EnphaseBaseURL = server.URL
	defer func() { EnphaseBaseURL = origURL }()

	_, err := AuthenticateWithEnphase("bad", "creds", "12345")
	assert.Error(t, err)
}

func TestAuthenticateWithEnphase_JSONResponse(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/login/login":
			w.WriteHeader(http.StatusOK)
		case "/entrez-auth-token":
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprint(w, `{"generation_time":1000000,"token":"jwt-from-json","expires_at":9999999}`)
		}
	}))
	defer server.Close()

	origURL := EnphaseBaseURL
	EnphaseBaseURL = server.URL
	defer func() { EnphaseBaseURL = origURL }()

	token, err := AuthenticateWithEnphase("user", "pass", "serial")
	require.NoError(t, err)
	assert.Equal(t, "jwt-from-json", token)
}

func TestParseJWTExpiry(t *testing.T) {
	future := time.Now().Add(24 * time.Hour).Truncate(time.Second)
	raw := makeTestJWT(future)

	expiry, err := parseJWTExpiry(raw)
	require.NoError(t, err)
	assert.Equal(t, future, expiry)
}

func TestParseJWTExpiry_InvalidToken(t *testing.T) {
	_, err := parseJWTExpiry("not-a-jwt")
	assert.Error(t, err)
}

func TestJWTRefresher_RefreshesExpiredToken(t *testing.T) {
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

	// Should eventually succeed after one failure (context allows up to 2s).
	select {
	case <-reconnectCh:
		assert.GreaterOrEqual(t, attempts, 2)
	case <-ctx.Done():
		t.Fatal("JWT refresh did not succeed within timeout")
	}
}

func TestPersistJWTToConfig_UpdatesExistingField(t *testing.T) {
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
	err := persistJWTToConfig("/nonexistent/path/config.yaml", "token")
	assert.Error(t, err)
}

func TestJWTRefresher_PersistsCalled(t *testing.T) {
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

func TestParseLogLevel(t *testing.T) {
	tests := []struct {
		input     string
		wantLevel slog.Level
		wantErr   bool
	}{
		{"", slog.LevelInfo, false},
		{"info", slog.LevelInfo, false},
		{"INFO", slog.LevelInfo, false},
		{"debug", slog.LevelDebug, false},
		{"DEBUG", slog.LevelDebug, false},
		{"warn", slog.LevelWarn, false},
		{"warning", slog.LevelWarn, false},
		{"error", slog.LevelError, false},
		{"ERROR", slog.LevelError, false},
		{"  debug  ", slog.LevelDebug, false}, // whitespace trimmed
		{"verbose", slog.LevelInfo, true},
		{"trace", slog.LevelInfo, true},
	}
	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
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
	err := run([]string{"-config", "/nonexistent/path.yaml"})
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "failed to load config")
}

func TestRun_ValidationFail(t *testing.T) {
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
