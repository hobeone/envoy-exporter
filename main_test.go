package main

import (
	"context"
	"errors"
	"testing"

	influxdb2write "github.com/influxdata/influxdb-client-go/v2/api/write"
	envoy "github.com/loafoe/go-envoy"
	"github.com/stretchr/testify/assert"
)

// MockEnvoyClient is a mock of EnvoyClient.
// It is defined in the test file because it's only used for testing.
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

type MockPointWriter struct {
	WritePointFunc func(ctx context.Context, point ...*influxdb2write.Point) error
}

func (m *MockPointWriter) WritePoint(ctx context.Context, point ...*influxdb2write.Point) error {
	if m.WritePointFunc != nil {
		return m.WritePointFunc(ctx, point...)
	}
	return nil
}

func TestConfigValidate(t *testing.T) {
	tests := []struct {
		name    string
		config  Config
		wantErr bool
	}{
		{
			name: "Valid Config",
			config: Config{
				Address:        "http://localhost",
				SerialNumber:   "12345",
				Username:       "user",
				InfluxDB:       "http://influx",
				InfluxDBBucket: "bucket",
				InfluxDBToken:  "token",
				InfluxDBOrg:    "org",
			},
			wantErr: false,
		},
		{
			name: "Valid Config with JWT",
			config: Config{
				Address:        "http://localhost",
				SerialNumber:   "12345",
				JWT:            "token",
				InfluxDB:       "http://influx",
				InfluxDBBucket: "bucket",
				InfluxDBToken:  "token",
				InfluxDBOrg:    "org",
			},
			wantErr: false,
		},
		{
			name: "Missing Address",
			config: Config{
				SerialNumber:   "12345",
				Username:       "user",
				InfluxDB:       "http://influx",
				InfluxDBBucket: "bucket",
				InfluxDBToken:  "token",
				InfluxDBOrg:    "org",
			},
			wantErr: true,
		},
		{
			name: "Missing Serial",
			config: Config{
				Address:        "http://localhost",
				Username:       "user",
				InfluxDB:       "http://influx",
				InfluxDBBucket: "bucket",
				InfluxDBToken:  "token",
				InfluxDBOrg:    "org",
			},
			wantErr: true,
		},
		{
			name: "Missing Authentication",
			config: Config{
				Address:        "http://localhost",
				SerialNumber:   "12345",
				InfluxDB:       "http://influx",
				InfluxDBBucket: "bucket",
				InfluxDBToken:  "token",
				InfluxDBOrg:    "org",
			},
			wantErr: true,
		},
		{
			name: "Missing InfluxDB",
			config: Config{
				Address:        "http://localhost",
				SerialNumber:   "12345",
				Username:       "user",
				InfluxDBBucket: "bucket",
				InfluxDBToken:  "token",
				InfluxDBOrg:    "org",
			},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.config.Validate()
			if tt.wantErr {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
			}
		})
	}
}

func TestLineToPoint(t *testing.T) {
	line := envoy.Line{
		WNow:       100,
		ReactPwr:   200,
		ApprntPwr:  300,
		RmsCurrent: 400,
		RmsVoltage: 500,
	}
	point := lineToPoint("test-type", line, 1, "test")
	assert.NotNil(t, point)
	assert.Equal(t, "test-type-line1", point.Name())

	tags := make(map[string]string)
	for _, tag := range point.TagList() {
		tags[tag.Key] = tag.Value
	}
	assert.Equal(t, map[string]string{
		"source":           "test",
		"measurement-type": "test-type",
		"line-idx":         "1",
	}, tags)

	fields := make(map[string]interface{})
	for _, field := range point.FieldList() {
		fields[field.Key] = field.Value
	}
	assert.Equal(t, map[string]interface{}{
		"P":     float64(100),
		"Q":     float64(200),
		"S":     float64(300),
		"I_rms": float64(400),
		"V_rms": float64(500),
	}, fields)
}

func TestExtractProductionStats(t *testing.T) {
	prod := &envoy.ProductionResponse{
		Production: []envoy.Measurement{
			{
				MeasurementType: "production",
				Lines: []envoy.Line{
					{WNow: 100},
				},
			},
		},
		Consumption: []envoy.Measurement{
			{
				MeasurementType: "total-consumption",
				Lines: []envoy.Line{
					{WNow: 200},
				},
			},
			{
				MeasurementType: "net-consumption",
				Lines: []envoy.Line{
					{WNow: 300},
				},
			},
		},
	}
	points := extractProductionStats(prod, "test")
	assert.Len(t, points, 3)
	assert.Equal(t, "production-line0", points[0].Name())
	assert.Equal(t, "consumption-line0", points[1].Name())
	assert.Equal(t, "net-line0", points[2].Name())
}

func TestExtractInverterStats(t *testing.T) {
	inverters := &[]envoy.Inverter{
		{
			SerialNumber:    "123",
			LastReportWatts: 100,
		},
	}
	points := extractInverterStats(inverters, "test")
	assert.Len(t, points, 1)
	assert.Equal(t, "inverter-production-123", points[0].Name())

	tags := make(map[string]string)
	for _, tag := range points[0].TagList() {
		tags[tag.Key] = tag.Value
	}
	assert.Equal(t, map[string]string{
		"source":           "test",
		"measurement-type": "inverter",
		"serial":           "123",
	}, tags)

	fields := make(map[string]interface{})
	for _, field := range points[0].FieldList() {
		fields[field.Key] = field.Value
	}
	assert.Equal(t, map[string]interface{}{
		"P": int64(100),
	}, fields)
}

func TestExtractBatteryStats(t *testing.T) {
	batteries := &[]envoy.Battery{
		{
			SerialNum:   "456",
			PercentFull: 80,
			Temperature: 25,
		},
	}
	points := extractBatteryStats(batteries, "test")
	assert.Len(t, points, 1)
	assert.Equal(t, "battery-456", points[0].Name())

	tags := make(map[string]string)
	for _, tag := range points[0].TagList() {
		tags[tag.Key] = tag.Value
	}
	assert.Equal(t, map[string]string{
		"source":           "test",
		"measurement-type": "battery",
		"serial":           "456",
	}, tags)

	fields := make(map[string]interface{})
	for _, field := range points[0].FieldList() {
		fields[field.Key] = field.Value
	}
	assert.Equal(t, map[string]interface{}{
		"percent-full": int64(80),
		"temperature":  int64(25),
	}, fields)
}

func TestScrape(t *testing.T) {
	mockWriter := &MockPointWriter{}
	
	tests := []struct {
		name          string
		mockClient    *MockEnvoyClient
		expectedPoints int
	}{
		{
			name: "successful scrape",
			mockClient: &MockEnvoyClient{
				ProductionFunc: func() (*envoy.ProductionResponse, error) {
					return &envoy.ProductionResponse{
						Production: []envoy.Measurement{
							{
								MeasurementType: "production",
								Lines:           []envoy.Line{{WNow: 100}},
							},
						},
					}, nil
				},
				InvertersFunc: func() (*[]envoy.Inverter, error) {
					return &[]envoy.Inverter{{
						SerialNumber:    "123",
						LastReportWatts: 100,
					}}, nil
				},
				BatteriesFunc: func() (*[]envoy.Battery, error) {
					return &[]envoy.Battery{{
						SerialNum:   "456",
						PercentFull: 80,
						Temperature: 25,
					}}, nil
				},
			},
			expectedPoints: 3,
		},
		{
			name: "production error",
			mockClient: &MockEnvoyClient{
				ProductionFunc: func() (*envoy.ProductionResponse, error) {
					return nil, errors.New("production error")
				},
			},
			expectedPoints: 0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			numPoints := scrape(context.Background(), tt.mockClient, mockWriter, "test")
			assert.Equal(t, tt.expectedPoints, numPoints)
		})
	}
}
