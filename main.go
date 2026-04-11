package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"syscall"
	"time"

	gateway "github.com/hobeone/enphase-gateway"
	influxdb2 "github.com/influxdata/influxdb-client-go/v2"
	yaml "gopkg.in/yaml.v3"
)

// gatewayAdapter wraps a gateway.Client and implements EnvoyClient.
// It caches the EID→MeasurementType mapping from the meter configuration
// on the first call to TypedMeterReadings.
type gatewayAdapter struct {
	client     *gateway.Client
	mu         sync.Mutex
	meterTypes map[int64]string // EID → MeasurementType; nil until first load
}

func (a *gatewayAdapter) LiveData(ctx context.Context) (gateway.LiveData, error) {
	return a.client.LiveData(ctx)
}

func (a *gatewayAdapter) Inverters(ctx context.Context) ([]gateway.InverterReading, error) {
	return a.client.Inverters(ctx)
}

func (a *gatewayAdapter) TypedMeterReadings(ctx context.Context) ([]TypedCTReading, error) {
	a.mu.Lock()
	meterTypes := a.meterTypes
	a.mu.Unlock()

	if meterTypes == nil {
		meters, err := a.client.Meters(ctx)
		if err != nil {
			return nil, fmt.Errorf("fetch meter config: %w", err)
		}
		meterTypes = make(map[int64]string, len(meters))
		for _, m := range meters {
			meterTypes[m.EID] = m.MeasurementType
		}
		a.mu.Lock()
		a.meterTypes = meterTypes
		a.mu.Unlock()
	}

	readings, err := a.client.MeterReadings(ctx)
	if err != nil {
		return nil, err
	}

	result := make([]TypedCTReading, len(readings))
	for i, r := range readings {
		result[i] = TypedCTReading{
			CTReading:       r,
			MeasurementType: meterTypes[r.EID],
		}
	}
	return result, nil
}

func defaultClientFactory(cfg *Config) (EnvoyClient, error) {
	client := gateway.NewClient(cfg.Address, cfg.JWT)
	return &gatewayAdapter{client: client}, nil
}

// AuthenticateWithEnphase fetches a JWT from Enphase cloud using username/password
// credentials. Delegates to gateway.FetchJWT which implements the two-step
// Enlighten login + Entrez token exchange documented in the IQ Gateway API spec.
func AuthenticateWithEnphase(username, password, serial string, opts ...gateway.AuthOption) (string, error) {
	tr, err := gateway.FetchJWT(context.Background(), username, password, serial, opts...)
	if err != nil {
		return "", err
	}
	return tr.Token, nil
}

// parseJWTExpiry extracts the expiry time from a raw JWT without signature verification.
func parseJWTExpiry(rawToken string) (time.Time, error) {
	return gateway.ParseExpiry(rawToken)
}

// persistJWTToConfig updates the jwt field in the YAML config file at path
// without disturbing other fields, comments, or formatting.
// The write is atomic: it goes to a temp file in the same directory, then
// os.Rename replaces the original, so a crash mid-write cannot corrupt it.
func persistJWTToConfig(path, token string) error {
	slog.Debug("Persisting JWT to config file", "file", path)
	info, err := os.Stat(path)
	if err != nil {
		return fmt.Errorf("stat config file: %w", err)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("read config file: %w", err)
	}

	var doc yaml.Node
	if err := yaml.Unmarshal(data, &doc); err != nil {
		return fmt.Errorf("parse config file: %w", err)
	}
	if len(doc.Content) == 0 {
		return fmt.Errorf("config file is empty")
	}
	mapping := doc.Content[0] // top-level mapping node

	// Find an existing jwt key and update its value in-place.
	found := false
	for i := 0; i+1 < len(mapping.Content); i += 2 {
		if mapping.Content[i].Value == "jwt" {
			mapping.Content[i+1].Value = token
			found = true
			break
		}
	}
	// If the jwt key was absent, append it.
	if !found {
		mapping.Content = append(mapping.Content,
			&yaml.Node{Kind: yaml.ScalarNode, Value: "jwt", Tag: "!!str"},
			&yaml.Node{Kind: yaml.ScalarNode, Value: token, Tag: "!!str"},
		)
	}

	out, err := yaml.Marshal(&doc)
	if err != nil {
		return fmt.Errorf("marshal config: %w", err)
	}

	// Atomic write: temp file in the same directory → rename.
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, ".envoy-jwt-*.tmp")
	if err != nil {
		return fmt.Errorf("create temp file: %w", err)
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName) // no-op after a successful rename

	if err := tmp.Chmod(info.Mode()); err != nil {
		tmp.Close()
		return fmt.Errorf("chmod temp file: %w", err)
	}
	if _, err := tmp.Write(out); err != nil {
		tmp.Close()
		return fmt.Errorf("write temp file: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("close temp file: %w", err)
	}
	if err := os.Rename(tmpName, path); err != nil {
		return err
	}
	slog.Info("JWT persisted to config file", "file", path)
	return nil
}

// tokenFetcher is a function that obtains a fresh JWT from Enphase.
type tokenFetcher func(username, password, serial string, opts ...gateway.AuthOption) (string, error)

// fetchWithRetry calls fetch repeatedly until it succeeds or ctx is cancelled.
// It waits retryWait between attempts.
func fetchWithRetry(ctx context.Context, cfg *Config, retryWait time.Duration, fetch tokenFetcher) (string, error) {
	for {
		token, err := fetch(cfg.Username, cfg.Password, cfg.SerialNumber)
		if err == nil {
			return token, nil
		}
		slog.Error("JWT refresh failed; retrying", "error", err)
		select {
		case <-ctx.Done():
			return "", ctx.Err()
		case <-time.After(retryWait):
		}
	}
}

// jwtRefresher runs in a background goroutine, proactively refreshing the JWT
// before expiry. On success it updates cfg.JWT, calls persist (if non-nil) to
// write the new token to the config file, and signals reconnectCh so that
// scrapeLoop can reconnect with the new token.
func jwtRefresher(ctx context.Context, cfg *Config, expiry time.Time, reconnectCh chan<- struct{}, fetch tokenFetcher, persist func(string) error) {
	leadTime := time.Duration(cfg.JWTRefreshLeadTime) * time.Minute
	if leadTime == 0 {
		leadTime = 60 * time.Minute
	}
	retryWait := time.Duration(cfg.RetryInterval) * time.Second
	if retryWait == 0 {
		retryWait = 5 * time.Second
	}

	for {
		delay := time.Until(expiry.Add(-leadTime))
		if delay < 0 {
			delay = 0
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(delay):
		}

		newToken, err := fetchWithRetry(ctx, cfg, retryWait, fetch)
		if err != nil {
			return // ctx cancelled
		}

		cfg.JWT = newToken
		slog.Info("JWT refreshed successfully")

		if persist != nil {
			if err := persist(newToken); err != nil {
				slog.Error("Failed to persist JWT to config file", "error", err)
			}
		}

		// Non-blocking send: if a reconnect is already pending, don't queue another.
		select {
		case reconnectCh <- struct{}{}:
		default:
		}

		newExpiry, err := parseJWTExpiry(newToken)
		if err != nil {
			slog.Warn("Could not parse refreshed JWT expiry; refresh disabled", "error", err)
			return
		}
		expiry = newExpiry
	}
}

// parseLogLevel converts a level name string to the corresponding slog.Level.
// An empty string maps to slog.LevelInfo (the default).
func parseLogLevel(s string) (slog.Level, error) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "", "info":
		return slog.LevelInfo, nil
	case "debug":
		return slog.LevelDebug, nil
	case "warn", "warning":
		return slog.LevelWarn, nil
	case "error":
		return slog.LevelError, nil
	default:
		return slog.LevelInfo, fmt.Errorf("unknown log level %q; use debug, info, warn, or error", s)
	}
}

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}
}

func run(args []string) error {
	fs := flag.NewFlagSet("envoy-exporter", flag.ContinueOnError)
	var cfgFile string
	var debug bool
	var logLevelFlag string
	var persistJWTFlag bool
	fs.StringVar(&cfgFile, "config", "envoy.yaml", "Path to config file.")
	fs.BoolVar(&debug, "debug", false, "Shorthand for -log-level debug.")
	fs.StringVar(&logLevelFlag, "log-level", "", "Log level: debug, info, warn, error (default: from config or \"info\").")
	fs.BoolVar(&persistJWTFlag, "persist-jwt", false, "Persist refreshed JWT back to the config file (overrides persist_jwt in config).")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if debug && logLevelFlag == "" {
		logLevelFlag = "debug"
	}

	// Bootstrap at info so startup messages are always visible.
	slog.SetDefault(slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo})))

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, os.Interrupt, syscall.SIGTERM)
	go func() {
		<-sigCh
		slog.Info("Received shutdown signal")
		cancel()
	}()

	slog.Info("Reading config", "file", cfgFile)
	cfg, err := LoadConfig(cfgFile)
	if err != nil {
		return fmt.Errorf("failed to load config: %w", err)
	}
	if err := cfg.Validate(); err != nil {
		return fmt.Errorf("configuration validation failed: %w", err)
	}

	// Resolve final log level: CLI flag > config file > "info" default.
	// Re-initialise the logger now that the config is available.
	if logLevelFlag == "" {
		logLevelFlag = cfg.LogLevel
	}
	level, err := parseLogLevel(logLevelFlag)
	if err != nil {
		return err
	}
	slog.SetDefault(slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: level})))
	slog.Debug("Logger configured", "level", logLevelFlag)

	// Build the persist function once; used at both the initial fetch and on refresh.
	var persistFn func(string) error
	if persistJWTFlag || cfg.PersistJWT {
		persistFn = func(token string) error {
			return persistJWTToConfig(cfgFile, token)
		}
	}

	// Auto-fetch JWT if credentials are present but no token was supplied.
	if cfg.JWT == "" && cfg.Username != "" && cfg.Password != "" {
		slog.Info("Fetching JWT from Enphase...")
		token, err := AuthenticateWithEnphase(cfg.Username, cfg.Password, cfg.SerialNumber)
		if err != nil {
			return fmt.Errorf("JWT auto-fetch failed: %w", err)
		}
		slog.Info("JWT obtained successfully")
		cfg.JWT = token
		if persistFn != nil {
			if err := persistFn(token); err != nil {
				slog.Error("Failed to persist initial JWT to config file", "error", err)
			}
		}
	}

	// Parse JWT expiry and start proactive refresh if credentials are available.
	var reconnectCh chan struct{}
	if cfg.JWT != "" {
		expiry, err := parseJWTExpiry(cfg.JWT)
		if err != nil {
			slog.Warn("Could not parse JWT expiry", "error", err)
		} else {
			slog.Info("JWT expires", "at", expiry.Format(time.RFC3339))
			if cfg.Username == "" || cfg.Password == "" {
				slog.Warn("No credentials configured; JWT expiry will not be handled automatically")
			} else {
				reconnectCh = make(chan struct{}, 1)
				go jwtRefresher(ctx, cfg, expiry, reconnectCh, AuthenticateWithEnphase, persistFn)
			}
		}
	}

	slog.Info("Starting Envoy Exporter",
		"go_version", runtime.Version(),
		"address", cfg.Address,
		"serial", cfg.SerialNumber,
		"interval_s", cfg.Interval,
		"source", cfg.SourceTag,
		"influxdb", cfg.InfluxDB,
		"influxdb_org", cfg.InfluxDBOrg,
		"influxdb_bucket", cfg.InfluxDBBucket,
		"log_level", logLevelFlag,
		"persist_jwt", persistJWTFlag || cfg.PersistJWT)

	influxClient := influxdb2.NewClient(cfg.InfluxDB, cfg.InfluxDBToken)
	defer influxClient.Close()
	writeAPI := influxClient.WriteAPIBlocking(cfg.InfluxDBOrg, cfg.InfluxDBBucket)

	scrapeLoop(ctx, cfg, writeAPI, defaultClientFactory, reconnectCh)
	return nil
}
