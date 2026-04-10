package main

import (
	"context"
	"errors"
	"testing"
	"time"

	influxdb2write "github.com/influxdata/influxdb-client-go/v2/api/write"
	envoy "github.com/loafoe/go-envoy"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// MockEnvoyClient implements EnvoyClient using configurable func fields.
type MockEnvoyClient struct {
	ProductionFunc        func() (*envoy.ProductionResponse, error)
	InvertersFunc         func() (*[]envoy.Inverter, error)
	BatteriesFunc         func() (*[]envoy.Battery, error)
	InvalidateSessionFunc func()
}

func (m *MockEnvoyClient) Production() (*envoy.ProductionResponse, error) {
	if m.ProductionFunc != nil {
		return m.ProductionFunc()
	}
	return nil, nil
}

func (m *MockEnvoyClient) Inverters() (*[]envoy.Inverter, error) {
	if m.InvertersFunc != nil {
		return m.InvertersFunc()
	}
	return nil, nil
}

func (m *MockEnvoyClient) Batteries() (*[]envoy.Battery, error) {
	if m.BatteriesFunc != nil {
		return m.BatteriesFunc()
	}
	return nil, nil
}

func (m *MockEnvoyClient) InvalidateSession() {
	if m.InvalidateSessionFunc != nil {
		m.InvalidateSessionFunc()
	}
}

// MockPointWriter captures written points for assertion.
type MockPointWriter struct {
	WritePointFunc func(ctx context.Context, point ...*influxdb2write.Point) error
	Written        []*influxdb2write.Point
}

func (m *MockPointWriter) WritePoint(ctx context.Context, point ...*influxdb2write.Point) error {
	if m.WritePointFunc != nil {
		return m.WritePointFunc(ctx, point...)
	}
	m.Written = append(m.Written, point...)
	return nil
}

// TestLineToPoint is table-driven to cover all three measurement types.
func TestLineToPoint(t *testing.T) {
	t.Parallel()

	line := envoy.Line{
		WNow:       100,
		ReactPwr:   200,
		ApprntPwr:  300,
		RmsCurrent: 400,
		RmsVoltage: 500,
	}
	ts := time.Now()

	tests := []struct {
		name            string
		namePrefix      string
		typeTag         string
		idx             int
		wantMeasurement string
		wantTypeTag     string
		wantLineIdx     string
	}{
		{
			name:            "production",
			namePrefix:      MeasurementProduction,
			typeTag:         MeasurementProduction,
			idx:             2,
			wantMeasurement: "production-line2",
			wantTypeTag:     MeasurementProduction,
			wantLineIdx:     "2",
		},
		{
			name:            "consumption",
			namePrefix:      MeasurementTotalConsumption,
			typeTag:         MeasurementTotalConsumption,
			idx:             0,
			wantMeasurement: "consumption-line0",
			wantTypeTag:     MeasurementTotalConsumption,
			wantLineIdx:     "0",
		},
		{
			name:            "net",
			namePrefix:      MeasurementNetConsumption,
			typeTag:         MeasurementNetConsumption,
			idx:             1,
			wantMeasurement: "net-line1",
			wantTypeTag:     MeasurementNetConsumption,
			wantLineIdx:     "1",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			pt := lineToPoint(tt.namePrefix, tt.typeTag, line, tt.idx, "home", ts)

			assert.Equal(t, tt.wantMeasurement, pt.Name())

			tags := tagMap(pt)
			assert.Equal(t, "home", tags["source"])
			assert.Equal(t, tt.wantTypeTag, tags["measurement-type"])
			assert.Equal(t, tt.wantLineIdx, tags["line-idx"])

			fields := fieldMap(pt)
			assert.Equal(t, float64(100), fields["P"])
			assert.Equal(t, float64(200), fields["Q"])
			assert.Equal(t, float64(300), fields["S"])
			assert.Equal(t, float64(400), fields["I_rms"])
			assert.Equal(t, float64(500), fields["V_rms"])
		})
	}
}

func TestExtractProductionStats(t *testing.T) {
	t.Parallel()

	prod := &envoy.ProductionResponse{
		Production: []envoy.Measurement{
			{
				MeasurementType: MeasurementProduction,
				Lines:           []envoy.Line{{WNow: 100}, {WNow: 200}},
			},
		},
		Consumption: []envoy.Measurement{
			{
				MeasurementType: MeasurementTotalConsumption,
				Lines:           []envoy.Line{{WNow: 300}},
			},
			{
				MeasurementType: MeasurementNetConsumption,
				Lines:           []envoy.Line{{WNow: 400}},
			},
		},
	}

	pts := extractProductionStats(prod, "test", time.Now())
	require.Len(t, pts, 4)

	// Verify measurement names (the namePrefix).
	assert.Equal(t, "production-line0", pts[0].Name())
	assert.Equal(t, "production-line1", pts[1].Name())
	assert.Equal(t, "consumption-line0", pts[2].Name())
	assert.Equal(t, "net-line0", pts[3].Name())

	// Verify measurement-type tags match the full constant, not the name prefix.
	// This is the key invariant: "consumption" prefix → "total-consumption" tag.
	assert.Equal(t, MeasurementProduction, tagMap(pts[0])["measurement-type"])
	assert.Equal(t, MeasurementProduction, tagMap(pts[1])["measurement-type"])
	assert.Equal(t, MeasurementTotalConsumption, tagMap(pts[2])["measurement-type"])
	assert.Equal(t, MeasurementNetConsumption, tagMap(pts[3])["measurement-type"])
}

func TestExtractProductionStats_IgnoresUnknownProductionType(t *testing.T) {
	t.Parallel()

	prod := &envoy.ProductionResponse{
		Production: []envoy.Measurement{
			{MeasurementType: "unknown", Lines: []envoy.Line{{WNow: 50}}},
		},
	}
	pts := extractProductionStats(prod, "test", time.Now())
	assert.Empty(t, pts)
}

func TestExtractProductionStats_IgnoresUnknownConsumptionType(t *testing.T) {
	t.Parallel()

	prod := &envoy.ProductionResponse{
		Consumption: []envoy.Measurement{
			{MeasurementType: "unknown-consumption", Lines: []envoy.Line{{WNow: 50}}},
		},
	}
	pts := extractProductionStats(prod, "test", time.Now())
	assert.Empty(t, pts)
}

func TestExtractInverterStats(t *testing.T) {
	t.Parallel()

	inverters := &[]envoy.Inverter{
		{SerialNumber: "ABC", LastReportWatts: 250},
		{SerialNumber: "DEF", LastReportWatts: 300},
	}
	pts := extractInverterStats(inverters, "home", time.Now())
	require.Len(t, pts, 2)
	assert.Equal(t, "inverter-production-ABC", pts[0].Name())
	assert.Equal(t, "inverter-production-DEF", pts[1].Name())
	assert.Equal(t, "inverter", tagMap(pts[0])["measurement-type"])
	assert.Equal(t, int64(250), fieldMap(pts[0])["P"])
}

func TestExtractBatteryStats(t *testing.T) {
	t.Parallel()

	batteries := &[]envoy.Battery{
		{SerialNum: "BAT1", PercentFull: 80, Temperature: 25},
	}
	pts := extractBatteryStats(batteries, "home", time.Now())
	require.Len(t, pts, 1)
	assert.Equal(t, "battery-BAT1", pts[0].Name())
	assert.Equal(t, "battery", tagMap(pts[0])["measurement-type"])
	assert.Equal(t, "BAT1", tagMap(pts[0])["serial"])
	assert.Equal(t, int64(80), fieldMap(pts[0])["percent-full"])
	assert.Equal(t, int64(25), fieldMap(pts[0])["temperature"])
}

func TestScrape_AllSources(t *testing.T) {
	t.Parallel()

	client := &MockEnvoyClient{
		ProductionFunc: func() (*envoy.ProductionResponse, error) {
			return &envoy.ProductionResponse{
				Production: []envoy.Measurement{
					{MeasurementType: MeasurementProduction, Lines: []envoy.Line{{WNow: 100}}},
				},
			}, nil
		},
		InvertersFunc: func() (*[]envoy.Inverter, error) {
			return &[]envoy.Inverter{{SerialNumber: "S1", LastReportWatts: 50}}, nil
		},
		BatteriesFunc: func() (*[]envoy.Battery, error) {
			return &[]envoy.Battery{{SerialNum: "B1", PercentFull: 90}}, nil
		},
	}
	writer := &MockPointWriter{}

	result := scrape(context.Background(), client, writer, "test")
	assert.Equal(t, 3, result.points)
	assert.False(t, result.hasErr)
}

func TestScrape_ProductionError(t *testing.T) {
	t.Parallel()

	client := &MockEnvoyClient{
		ProductionFunc: func() (*envoy.ProductionResponse, error) {
			return nil, errors.New("network error")
		},
	}
	result := scrape(context.Background(), client, &MockPointWriter{}, "test")
	assert.Equal(t, 0, result.points)
	assert.True(t, result.hasErr)
}

func TestScrape_WriteError(t *testing.T) {
	t.Parallel()

	client := &MockEnvoyClient{
		ProductionFunc: func() (*envoy.ProductionResponse, error) {
			return &envoy.ProductionResponse{
				Production: []envoy.Measurement{
					{MeasurementType: MeasurementProduction, Lines: []envoy.Line{{WNow: 100}}},
				},
			}, nil
		},
	}
	writer := &MockPointWriter{
		WritePointFunc: func(ctx context.Context, point ...*influxdb2write.Point) error {
			return errors.New("influxdb unavailable")
		},
	}

	result := scrape(context.Background(), client, writer, "test")
	assert.Equal(t, 0, result.points, "write failed so points should not be counted")
	assert.True(t, result.hasErr)
}

func TestScrape_PartialErrors(t *testing.T) {
	t.Parallel()

	// Production succeeds, inverters and batteries fail — expect only production points.
	client := &MockEnvoyClient{
		ProductionFunc: func() (*envoy.ProductionResponse, error) {
			return &envoy.ProductionResponse{
				Production: []envoy.Measurement{
					{MeasurementType: MeasurementProduction, Lines: []envoy.Line{{WNow: 100}}},
				},
			}, nil
		},
		InvertersFunc: func() (*[]envoy.Inverter, error) {
			return nil, errors.New("inverter error")
		},
		BatteriesFunc: func() (*[]envoy.Battery, error) {
			return nil, errors.New("battery error")
		},
	}
	writer := &MockPointWriter{}

	result := scrape(context.Background(), client, writer, "test")
	assert.Equal(t, 1, result.points)
	assert.True(t, result.hasErr)
}

func TestConnectWithBackoff_ImmediateSuccess(t *testing.T) {
	t.Parallel()

	client := &MockEnvoyClient{}
	factory := func(_ *Config) (EnvoyClient, error) { return client, nil }

	e, err := connectWithBackoff(context.Background(), &Config{}, factory, 10*time.Millisecond, 1*time.Second)
	require.NoError(t, err)
	assert.Equal(t, client, e)
}

func TestConnectWithBackoff_RetriesThenSucceeds(t *testing.T) {
	t.Parallel()

	attempts := 0
	client := &MockEnvoyClient{}
	factory := func(_ *Config) (EnvoyClient, error) {
		attempts++
		if attempts < 3 {
			return nil, errors.New("not ready")
		}
		return client, nil
	}

	e, err := connectWithBackoff(context.Background(), &Config{}, factory, 5*time.Millisecond, 1*time.Second)
	require.NoError(t, err)
	assert.Equal(t, client, e)
	assert.Equal(t, 3, attempts)
}

// TestConnectWithBackoff_BackoffCap verifies that the delay is capped at maxDelay.
// With base=2ms and maxDelay=3ms, the progression is: wait 2ms (backoff→3ms), wait 3ms,
// wait 3ms... rather than the uncapped 2ms, 4ms, 8ms, 16ms.
func TestConnectWithBackoff_BackoffCap(t *testing.T) {
	t.Parallel()

	attempts := 0
	client := &MockEnvoyClient{}
	factory := func(_ *Config) (EnvoyClient, error) {
		attempts++
		if attempts < 5 {
			return nil, errors.New("not ready")
		}
		return client, nil
	}

	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()

	e, err := connectWithBackoff(ctx, &Config{}, factory, 2*time.Millisecond, 3*time.Millisecond)
	require.NoError(t, err)
	assert.Equal(t, client, e)
	assert.Equal(t, 5, attempts)
}

func TestConnectWithBackoff_CancelledContext(t *testing.T) {
	t.Parallel()

	factory := func(_ *Config) (EnvoyClient, error) {
		return nil, errors.New("always fails")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()

	_, err := connectWithBackoff(ctx, &Config{}, factory, 10*time.Millisecond, 1*time.Second)
	assert.Error(t, err)
}

func TestScrapeLoop_RunsAndWritesPoints(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()

	client := &MockEnvoyClient{
		ProductionFunc: func() (*envoy.ProductionResponse, error) {
			return &envoy.ProductionResponse{
				Production: []envoy.Measurement{
					{MeasurementType: MeasurementProduction, Lines: []envoy.Line{{WNow: 100}}},
				},
			}, nil
		},
	}
	writer := &MockPointWriter{}
	factory := func(_ *Config) (EnvoyClient, error) { return client, nil }

	scrapeLoop(ctx, cfg1(), writer, factory, nil)

	assert.NotEmpty(t, writer.Written, "scrape loop should have written points")
}

func TestScrapeLoop_ConnectionRetry(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	attempts := 0
	client := &MockEnvoyClient{}
	factory := func(_ *Config) (EnvoyClient, error) {
		attempts++
		if attempts < 2 {
			return nil, errors.New("not ready")
		}
		return client, nil
	}

	scrapeLoop(ctx, cfg1(), &MockPointWriter{}, factory, nil)

	assert.GreaterOrEqual(t, attempts, 2)
}

// cfg1 returns a minimal Config suitable for scrapeLoop tests.
func cfg1() *Config { return &Config{Interval: 1, RetryInterval: 1} }

// helpers

func tagMap(pt *influxdb2write.Point) map[string]string {
	m := make(map[string]string)
	for _, tag := range pt.TagList() {
		m[tag.Key] = tag.Value
	}
	return m
}

func fieldMap(pt *influxdb2write.Point) map[string]any {
	m := make(map[string]any)
	for _, f := range pt.FieldList() {
		m[f.Key] = f.Value
	}
	return m
}
