package main

import (
	"errors"
	"testing"

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

func TestLineToPoint(t *testing.T) {
	cfg.SourceTag = "test"
	line := envoy.Line{
		WNow:       100,
		ReactPwr:   200,
		ApprntPwr:  300,
		RmsCurrent: 400,
		RmsVoltage: 500,
	}
	point := lineToPoint("test-type", line, 1)
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
	cfg.SourceTag = "test"
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
	points := extractProductionStats(prod)
	assert.Len(t, points, 3)
	assert.Equal(t, "production-line0", points[0].Name())
	assert.Equal(t, "consumption-line0", points[1].Name())
	assert.Equal(t, "net-line0", points[2].Name())
}

func TestExtractInverterStats(t *testing.T) {
	cfg.SourceTag = "test"
	inverters := &[]envoy.Inverter{
		{
			SerialNumber:    "123",
			LastReportWatts: 100,
		},
	}
	points := extractInverterStats(inverters)
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
	cfg.SourceTag = "test"
	batteries := &[]envoy.Battery{
		{
			SerialNum:   "456",
			PercentFull: 80,
			Temperature: 25,
		},
	}
	points := extractBatteryStats(batteries)
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
	cfg.SourceTag = "test"
	cfg.InfluxDB = "http://localhost:8086"
	cfg.InfluxDBToken = "test-token"
	cfg.InfluxDBOrg = "test-org"
	cfg.InfluxDBBucket = "test-bucket"

	tests := []struct {
		name          string
		mockClient    *MockEnvoyClient
		expectedError bool
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
			expectedError: false,
		},
		{
			name: "production error",
			mockClient: &MockEnvoyClient{
				ProductionFunc: func() (*envoy.ProductionResponse, error) {
					return nil, errors.New("production error")
				},
			},
			expectedError: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			numPoints := scrape(tt.mockClient)
			if tt.expectedError {
				assert.Equal(t, 0, numPoints)
			} else {
				assert.Equal(t, 3, numPoints)
			}
		})
	}
}
