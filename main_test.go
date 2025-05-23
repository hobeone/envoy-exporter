package main

import (
	"context"
	"fmt"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"

	influxdb2 "github.com/influxdata/influxdb-client-go/v2"
	influxdb2write "github.com/influxdata/influxdb-client-go/v2/api/write"
	influxdatalp "github.com/influxdata/line-protocol"
	envoy "github.com/loafoe/go-envoy"
)

// Helper to find a tag in a list of tags
func findTag(tags []*influxdatalp.Tag, key string) *influxdatalp.Tag {
	for _, tag := range tags {
		if tag.Key == key {
			return tag
		}
	}
	return nil
}

// Helper to find a field in a list of fields
func findField(fields []*influxdatalp.Field, key string) *influxdatalp.Field {
	for _, field := range fields {
		if field.Key == key {
			return field
		}
	}
	return nil
}

// comparePoints checks if two InfluxDB points are equivalent in terms of name, tags, fields, and time.
// It uses t.Errorf to report discrepancies.
func comparePoints(t *testing.T, expected, actual *influxdb2write.Point, checkTime bool) {
	t.Helper()

	if expected.Name() != actual.Name() {
		t.Errorf("Name mismatch: expected '%s', got '%s'", expected.Name(), actual.Name())
	}

	// Compare Tags
	if len(expected.TagList()) != len(actual.TagList()) {
		t.Errorf("Tag list length mismatch for point '%s': expected %d, got %d. Expected: %v, Actual: %v",
			expected.Name(), len(expected.TagList()), len(actual.TagList()), expected.TagList(), actual.TagList())
	} else {
		for _, expTag := range expected.TagList() {
			actTag := findTag(actual.TagList(), expTag.Key)
			if actTag == nil {
				t.Errorf("Expected tag key '%s' not found in actual point '%s'", expTag.Key, actual.Name())
				continue
			}
			if expTag.Value != actTag.Value {
				t.Errorf("Tag value mismatch for key '%s' in point '%s': expected '%s', got '%s'",
					expTag.Key, actual.Name(), expTag.Value, actTag.Value)
			}
		}
	}

	// Compare Fields
	if len(expected.FieldList()) != len(actual.FieldList()) {
		t.Errorf("Field list length mismatch for point '%s': expected %d, got %d. Expected: %v, Actual: %v",
			expected.Name(), len(expected.FieldList()), len(actual.FieldList()), expected.FieldList(), actual.FieldList())
	} else {
		for _, expField := range expected.FieldList() {
			actField := findField(actual.FieldList(), expField.Key)
			if actField == nil {
				t.Errorf("Expected field key '%s' not found in actual point '%s'", expField.Key, actual.Name())
				continue
			}
			// Comparing interface{} values can be tricky. Sprintf is a common way to get a consistent string representation.
			if fmt.Sprintf("%v", expField.Value) != fmt.Sprintf("%v", actField.Value) {
				t.Errorf("Field value mismatch for key '%s' in point '%s': expected '%v' (type %T), got '%v' (type %T)",
					expField.Key, actual.Name(), expField.Value, expField.Value, actField.Value, actField.Value)
			}
		}
	}

	if checkTime {
		if !expected.Time().Equal(actual.Time()) {
			t.Errorf("Timestamp mismatch for point '%s': expected %v, got %v", expected.Name(), expected.Time(), actual.Time())
		}
	}
}

// Helper to create temporary config files:
func createTempConfigFile(t *testing.T, content string) string {
	t.Helper()
	// Create a temporary directory for the config file
	// The t.TempDir() function automatically cleans up the directory after the test.
	tmpDir := t.TempDir()
	tmpFile, err := os.CreateTemp(tmpDir, "config-*.yaml")
	if err != nil {
		t.Fatalf("Failed to create temp config file: %v", err)
	}
	if _, err := tmpFile.WriteString(content); err != nil {
		t.Fatalf("Failed to write to temp config file: %v", err)
	}
	name := tmpFile.Name() // Get the name before closing
	if err := tmpFile.Close(); err != nil {
		t.Fatalf("Failed to close temp config file: %v", err)
	}
	return name
}

func TestLoadAndValidateConfig(t *testing.T) {
	validBaseConfig := `
address: "localhost:1234"
serial: "testserial"
username: "user"
password: "pw"
influxdb: "http://localhost:8086"
influxdb_token: "token"
influxdb_org: "org"
influxdb_bucket: "bucket"
source: "test-source-from-file"
`

	tests := []struct {
		name          string
		fileContent   string
		expectError   bool
		errorContains string
		expectedCfg   func() Config // Use a function to get expected config to handle default interval
		customChecks  func(t *testing.T, cfg Config, err error)
	}{
		{
			name:        "Valid Config with specific interval",
			fileContent: validBaseConfig + "interval: 10\n",
			expectError: false,
			expectedCfg: func() Config {
				return Config{
					Address:        "localhost:1234",
					SerialNumber:   "testserial",
					Username:       "user",
					Password:       "pw",
					InfluxDB:       "http://localhost:8086",
					InfluxDBToken:  "token",
					InfluxDBOrg:    "org",
					InfluxDBBucket: "bucket",
					SourceTag:      "test-source-from-file",
					Interval:       10,
				}
			},
		},
		{
			name:        "Valid Config with JWT auth",
			fileContent: "address: \"localhost:1234\"\nserial: \"testserial\"\njwt: \"testjwt\"\ninfluxdb: \"http://localhost:8086\"\ninfluxdb_token: \"token\"\ninfluxdb_org: \"org\"\ninfluxdb_bucket: \"bucket\"\ninterval: 7\n",
			expectError: false,
			expectedCfg: func() Config {
				return Config{
					Address:        "localhost:1234",
					SerialNumber:   "testserial",
					JWT:            "testjwt",
					InfluxDB:       "http://localhost:8086",
					InfluxDBToken:  "token",
					InfluxDBOrg:    "org",
					InfluxDBBucket: "bucket",
					Interval:       7,
				}
			},
		},
		{
			name:        "Default Interval",
			fileContent: validBaseConfig, // Interval not specified
			expectError: false,
			expectedCfg: func() Config {
				return Config{
					Address:        "localhost:1234",
					SerialNumber:   "testserial",
					Username:       "user",
					Password:       "pw",
					InfluxDB:       "http://localhost:8086",
					InfluxDBToken:  "token",
					InfluxDBOrg:    "org",
					InfluxDBBucket: "bucket",
					SourceTag:      "test-source-from-file",
					Interval:       5, // Default
				}
			},
		},
		{
			name:          "Missing Address",
			fileContent:   "serial: \"testserial\"\nusername: \"user\"\npassword: \"pw\"\ninfluxdb: \"http://localhost:8086\"\ninfluxdb_token: \"token\"\ninfluxdb_org: \"org\"\ninfluxdb_bucket: \"bucket\"\n",
			expectError:   true,
			errorContains: "Missing required configuration: address",
		},
		{
			name:          "Missing SerialNumber",
			fileContent:   "address: \"localhost:1234\"\nusername: \"user\"\npassword: \"pw\"\ninfluxdb: \"http://localhost:8086\"\ninfluxdb_token: \"token\"\ninfluxdb_org: \"org\"\ninfluxdb_bucket: \"bucket\"\n",
			expectError:   true,
			errorContains: "Missing required configuration: serial",
		},
		{
			name:          "Missing All Auth",
			fileContent:   "address: \"localhost:1234\"\nserial: \"testserial\"\nusername: \"\"\npassword: \"\"\njwt: \"\"\ninfluxdb: \"http://localhost:8086\"\ninfluxdb_token: \"token\"\ninfluxdb_org: \"org\"\ninfluxdb_bucket: \"bucket\"\n",
			expectError:   true,
			errorContains: "Missing Envoy authentication",
		},
		{
			name:          "Missing InfluxDB",
			fileContent:   strings.Replace(validBaseConfig, `influxdb: "http://localhost:8086"`, "influxdb: \"\"\n", 1),
			expectError:   true,
			errorContains: "Missing required configuration: influxdb",
		},
		{
			name:          "Missing InfluxDBToken",
			fileContent:   strings.Replace(validBaseConfig, `influxdb_token: "token"`, "influxdb_token: \"\"", 1),
			expectError:   true,
			errorContains: "Missing required configuration: influxdb_token",
		},
		{
			name:          "Missing InfluxDBOrg",
			fileContent:   strings.Replace(validBaseConfig, `influxdb_org: "org"`, "influxdb_org: \"\"", 1),
			expectError:   true,
			errorContains: "Missing required configuration: influxdb_org",
		},
		{
			name:          "Missing InfluxDBBucket",
			fileContent:   strings.Replace(validBaseConfig, `influxdb_bucket: "bucket"`, "influxdb_bucket: \"\"", 1),
			expectError:   true,
			errorContains: "Missing required configuration: influxdb_bucket",
		},
		{
			name:          "File Not Found",
			fileContent:   "", // No file will be created for this
			expectError:   true,
			errorContains: "opening config file", // Error from os.Open
			customChecks: func(t *testing.T, cfg Config, err error) {
				if err == nil || !strings.Contains(err.Error(), "no such file or directory") {
					t.Errorf("Expected 'no such file or directory' error, got: %v", err)
				}
			},
		},
		{
			name:          "Invalid YAML",
			fileContent:   "address: localhost:1234\n  serial: testserial\n- username: user", // Malformed
			expectError:   true,
			errorContains: "decoding config file",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var filePath string
			if tt.name == "File Not Found" {
				filePath = "non_existent_file.yaml"
			} else {
				filePath = createTempConfigFile(t, tt.fileContent)
			}

			cfg, err := loadAndValidateConfig(filePath)

			if tt.expectError {
				if err == nil {
					t.Fatalf("loadAndValidateConfig() expected error, got nil")
				}
				if tt.errorContains != "" && !strings.Contains(err.Error(), tt.errorContains) {
					t.Errorf("loadAndValidateConfig() error = %q, want error containing %q", err.Error(), tt.errorContains)
				}
			} else {
				if err != nil {
					t.Fatalf("loadAndValidateConfig() unexpected error: %v", err)
				}
				expected := tt.expectedCfg()
				if !reflect.DeepEqual(cfg, expected) {
					t.Errorf("loadAndValidateConfig() cfg = %+v, want %+v", cfg, expected)
				}
			}

			if tt.customChecks != nil {
				tt.customChecks(t, cfg, err)
			}
		})
	}
}

// Default mock implementations
func defaultCommCheck() (*envoy.CommCheckResponse, error) {
	return &envoy.CommCheckResponse{"device": 0}, nil
}

func TestProuctionScrape(t *testing.T) {
	cfg.SourceTag = "test-source" // Consistent source tag for tests
	fixedTime := time.Now().Truncate(time.Second)

	defaultProduction := func() (*envoy.ProductionResponse, error) {
		return &envoy.ProductionResponse{
			Production: []envoy.Measurement{{MeasurementType: "production", Lines: []envoy.Line{{WNow: 100}}}},
		}, nil
	}
	defaultInverters := func() (*[]envoy.Inverter, error) {
		return &[]envoy.Inverter{{SerialNumber: "inv1", LastReportWatts: 50}}, nil
	}
	defaultBatteries := func() (*[]envoy.Battery, error) {
		return &[]envoy.Battery{{SerialNum: "bat1", PercentFull: 75}}, nil
	}

	tests := []struct {
		name                      string
		setupMockEnvoy            func(*MockEnvoyClient)
		setupMockInflux           func(*MockInfluxWriter)
		expectedNumPoints         int
		expectedClientInvalidated bool
		expectedPointsWritten     int
		checkInvalidateCalled     bool
		expectedInvalidateCalled  bool
	}{
		{
			name: "Success Case - All data fetched and written",
			setupMockEnvoy: func(mec *MockEnvoyClient) {
				mec.CommCheckFunc = defaultCommCheck
				mec.ProductionFunc = defaultProduction
				mec.InvertersFunc = defaultInverters
				mec.BatteriesFunc = defaultBatteries
			},
			setupMockInflux: func(miw *MockInfluxWriter) {
				miw.WritePointError = nil
			},
			expectedNumPoints:         3, // 1 production, 1 inverter, 1 battery
			expectedClientInvalidated: false,
			expectedPointsWritten:     3,
			checkInvalidateCalled:     true,
			expectedInvalidateCalled:  false,
		},
		{
			name: "CommCheck Failure",
			setupMockEnvoy: func(mec *MockEnvoyClient) {
				mec.CommCheckFunc = func() (*envoy.CommCheckResponse, error) {
					return nil, fmt.Errorf("CommCheck error")
				}
				// Production, Inverters, Batteries should not be called
				mec.ProductionFunc = func() (*envoy.ProductionResponse, error) {
					t.Error("ProductionFunc should not be called when CommCheck fails")
					return nil, nil
				}
			},
			setupMockInflux:           func(miw *MockInfluxWriter) {},
			expectedNumPoints:         0,
			expectedClientInvalidated: true,
			expectedPointsWritten:     0,
			checkInvalidateCalled:     true,
			expectedInvalidateCalled:  true,
		},
		{
			name: "Production Data Fetch Failure",
			setupMockEnvoy: func(mec *MockEnvoyClient) {
				mec.CommCheckFunc = defaultCommCheck
				mec.ProductionFunc = func() (*envoy.ProductionResponse, error) {
					return nil, fmt.Errorf("Production error")
				}
				mec.InvertersFunc = defaultInverters // Inverters should still be called
				mec.BatteriesFunc = defaultBatteries // Batteries should still be called
			},
			setupMockInflux:           func(miw *MockInfluxWriter) {},
			expectedNumPoints:         2, // 1 inverter, 1 battery
			expectedClientInvalidated: false,
			expectedPointsWritten:     2,
			checkInvalidateCalled:     true,
			expectedInvalidateCalled:  false,
		},
		{
			name: "All Data Fetch Failures (CommCheck success)",
			setupMockEnvoy: func(mec *MockEnvoyClient) {
				mec.CommCheckFunc = defaultCommCheck
				mec.ProductionFunc = func() (*envoy.ProductionResponse, error) { return nil, fmt.Errorf("Prod error") }
				mec.InvertersFunc = func() (*[]envoy.Inverter, error) { return nil, fmt.Errorf("Inv error") }
				mec.BatteriesFunc = func() (*[]envoy.Battery, error) { return nil, fmt.Errorf("Bat error") }
			},
			setupMockInflux:           func(miw *MockInfluxWriter) {},
			expectedNumPoints:         0,
			expectedClientInvalidated: false,
			expectedPointsWritten:     0,
			checkInvalidateCalled:     true,
			expectedInvalidateCalled:  false,
		},
		{
			name: "No Data Found (Successful Fetch, Empty Results)",
			setupMockEnvoy: func(mec *MockEnvoyClient) {
				mec.CommCheckFunc = defaultCommCheck
				mec.ProductionFunc = func() (*envoy.ProductionResponse, error) { return &envoy.ProductionResponse{}, nil } // Empty
				mec.InvertersFunc = func() (*[]envoy.Inverter, error) { return &[]envoy.Inverter{}, nil }                  // Empty
				mec.BatteriesFunc = func() (*[]envoy.Battery, error) { return &[]envoy.Battery{}, nil }                    // Empty
			},
			setupMockInflux:           func(miw *MockInfluxWriter) {},
			expectedNumPoints:         0,
			expectedClientInvalidated: false,
			expectedPointsWritten:     0,
			checkInvalidateCalled:     true,
			expectedInvalidateCalled:  false,
		},
		{
			name: "InfluxDB WritePoint Failure",
			setupMockEnvoy: func(mec *MockEnvoyClient) {
				mec.CommCheckFunc = defaultCommCheck
				mec.ProductionFunc = defaultProduction
				mec.InvertersFunc = defaultInverters
				mec.BatteriesFunc = defaultBatteries
			},
			setupMockInflux: func(miw *MockInfluxWriter) {
				miw.WritePointError = fmt.Errorf("InfluxDB write error")
			},
			expectedNumPoints:         3, // Points are collected
			expectedClientInvalidated: false,
			expectedPointsWritten:     3, // WritePoint is attempted
			checkInvalidateCalled:     true,
			expectedInvalidateCalled:  false,
		},
		{
			name: "Partial Data (Only Production Available)",
			setupMockEnvoy: func(mec *MockEnvoyClient) {
				mec.CommCheckFunc = defaultCommCheck
				mec.ProductionFunc = defaultProduction
				mec.InvertersFunc = func() (*[]envoy.Inverter, error) { return nil, fmt.Errorf("Inverter error") }
				mec.BatteriesFunc = func() (*[]envoy.Battery, error) { return &[]envoy.Battery{}, nil } // Empty batteries
			},
			setupMockInflux:           func(miw *MockInfluxWriter) {},
			expectedNumPoints:         1, // Only 1 production point
			expectedClientInvalidated: false,
			expectedPointsWritten:     1,
			checkInvalidateCalled:     true,
			expectedInvalidateCalled:  false,
		},
		{
			name: "Production data has nil lines",
			setupMockEnvoy: func(mec *MockEnvoyClient) {
				mec.CommCheckFunc = defaultCommCheck
				mec.ProductionFunc = func() (*envoy.ProductionResponse, error) {
					return &envoy.ProductionResponse{
						Production: []envoy.Measurement{{MeasurementType: "production", Lines: nil}}, // Nil lines
					}, nil
				}
				mec.InvertersFunc = defaultInverters
				mec.BatteriesFunc = defaultBatteries
			},
			setupMockInflux:           func(miw *MockInfluxWriter) {},
			expectedNumPoints:         2, // Inverter + Battery
			expectedClientInvalidated: false,
			expectedPointsWritten:     2,
		},
		{
			name: "Production data has empty lines",
			setupMockEnvoy: func(mec *MockEnvoyClient) {
				mec.CommCheckFunc = defaultCommCheck
				mec.ProductionFunc = func() (*envoy.ProductionResponse, error) {
					return &envoy.ProductionResponse{
						Production: []envoy.Measurement{{MeasurementType: "production", Lines: []envoy.Line{}}}, // Empty lines
					}, nil
				}
				mec.InvertersFunc = defaultInverters
				mec.BatteriesFunc = defaultBatteries
			},
			setupMockInflux:           func(miw *MockInfluxWriter) {},
			expectedNumPoints:         2, // Inverter + Battery
			expectedClientInvalidated: false,
			expectedPointsWritten:     2,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mockEnvoy := newMockEnvoyClient()
			mockInflux := newMockInfluxWriter()

			if tt.setupMockEnvoy != nil {
				tt.setupMockEnvoy(mockEnvoy)
			}
			if tt.setupMockInflux != nil {
				tt.setupMockInflux(mockInflux)
			}

			numPoints, clientInvalidated := scrape(mockEnvoy, mockInflux, fixedTime)

			if numPoints != tt.expectedNumPoints {
				t.Errorf("scrape() numPoints = %v, want %v", numPoints, tt.expectedNumPoints)
			}
			if clientInvalidated != tt.expectedClientInvalidated {
				t.Errorf("scrape() clientInvalidated = %v, want %v", clientInvalidated, tt.expectedClientInvalidated)
			}
			if len(mockInflux.PointsWritten) != tt.expectedPointsWritten {
				t.Errorf("len(mockInflux.PointsWritten) = %v, want %v", len(mockInflux.PointsWritten), tt.expectedPointsWritten)
			}
			if tt.checkInvalidateCalled {
				if mockEnvoy.invalidateSessionCalled != tt.expectedInvalidateCalled {
					t.Errorf("mockEnvoy.invalidateSessionCalled = %v, want %v", mockEnvoy.invalidateSessionCalled, tt.expectedInvalidateCalled)
				}
			}

			// Verify timestamps if points were written
			if tt.expectedPointsWritten > 0 {
				for i, point := range mockInflux.PointsWritten {
					if !point.Time().Equal(fixedTime) {
						t.Errorf("Point %d timestamp = %v, want %v", i, point.Time(), fixedTime)
					}
				}
			}
		})
	}
}

func TestExtractBatteryStats(t *testing.T) {
	cfg.SourceTag = "test-source"
	now := time.Now().Truncate(time.Second)

	tests := []struct {
		name           string
		batteries      *[]envoy.Battery
		ts             time.Time
		expectedLen    int
		expectedPoints []struct {
			name   string
			tags   map[string]string
			fields map[string]interface{}
		}
	}{
		{
			name:        "Nil input",
			batteries:   nil,
			ts:          now,
			expectedLen: 0,
		},
		{
			name:        "Empty slice",
			batteries:   &[]envoy.Battery{},
			ts:          now,
			expectedLen: 0,
		},
		{
			name: "One battery",
			batteries: &[]envoy.Battery{
				{SerialNum: "bat789", PercentFull: 88, Temperature: 25},
			},
			ts:          now,
			expectedLen: 1,
			expectedPoints: []struct {
				name   string
				tags   map[string]string
				fields map[string]interface{}
			}{
				{
					name: "battery-bat789",
					tags: map[string]string{
						"source":           "test-source",
						"measurement-type": "battery",
						"serial":           "bat789",
					},
					fields: map[string]interface{}{"percent-full": 88, "temperature": 25},
				},
			},
		},
		{
			name: "Multiple batteries",
			batteries: &[]envoy.Battery{
				{SerialNum: "bat789", PercentFull: 88, Temperature: 25},
				{SerialNum: "bat123", PercentFull: 50, Temperature: 22},
				{SerialNum: "bat456", PercentFull: 100, Temperature: 28},
			},
			ts:          now,
			expectedLen: 3,
			expectedPoints: []struct {
				name   string
				tags   map[string]string
				fields map[string]interface{}
			}{
				{
					name:   "battery-bat789",
					tags:   map[string]string{"serial": "bat789"},
					fields: map[string]interface{}{"percent-full": 88, "temperature": 25},
				},
				{
					name:   "battery-bat123",
					tags:   map[string]string{"serial": "bat123"},
					fields: map[string]interface{}{"percent-full": 50, "temperature": 22},
				},
				{
					name:   "battery-bat456",
					tags:   map[string]string{"serial": "bat456"},
					fields: map[string]interface{}{"percent-full": 100, "temperature": 28},
				},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := extractBatteryStats(tt.batteries, tt.ts)

			if len(got) != tt.expectedLen {
				t.Errorf("extractBatteryStats() len = %v, want %v", len(got), tt.expectedLen)
			}

			if len(tt.expectedPoints) > 0 {
				for i, expectedPt := range tt.expectedPoints {
					if i >= len(got) {
						t.Errorf("Missing expected point: %s", expectedPt.name)
						continue
					}
					actualPt := got[i]
					expectedFullPoint := influxdb2.NewPointWithMeasurement(expectedPt.name)
					for k, v := range expectedPt.tags {
						expectedFullPoint.AddTag(k, v)
					}
					// Add common tags that are always expected
					expectedFullPoint.AddTag("source", cfg.SourceTag)
					expectedFullPoint.AddTag("measurement-type", "battery")

					for k, v := range expectedPt.fields {
						expectedFullPoint.AddField(k, v)
					}
					expectedFullPoint.SetTime(tt.ts)

					comparePoints(t, expectedFullPoint, actualPt, true)
				}
			}
		})
	}
}

func TestExtractInverterStats(t *testing.T) {
	cfg.SourceTag = "test-source"
	now := time.Now().Truncate(time.Second)

	tests := []struct {
		name           string
		inverters      *[]envoy.Inverter
		ts             time.Time
		expectedLen    int
		expectedPoints []struct {
			name   string
			tags   map[string]string
			fields map[string]interface{}
		}
	}{
		{
			name:        "Nil input",
			inverters:   nil,
			ts:          now,
			expectedLen: 0,
		},
		{
			name:        "Empty slice",
			inverters:   &[]envoy.Inverter{},
			ts:          now,
			expectedLen: 0,
		},
		{
			name: "One inverter",
			inverters: &[]envoy.Inverter{
				{SerialNumber: "inv123", LastReportWatts: 250},
			},
			ts:          now,
			expectedLen: 1,
			expectedPoints: []struct {
				name   string
				tags   map[string]string
				fields map[string]interface{}
			}{
				{
					name: "inverter-production-inv123",
					tags: map[string]string{
						"source":           "test-source",
						"measurement-type": "inverter",
						"serial":           "inv123",
					},
					fields: map[string]interface{}{"P": 250},
				},
			},
		},
		{
			name: "Multiple inverters",
			inverters: &[]envoy.Inverter{
				{SerialNumber: "inv123", LastReportWatts: 250},
				{SerialNumber: "inv456", LastReportWatts: 300},
				{SerialNumber: "inv789", LastReportWatts: 0}, // Zero watts
			},
			ts:          now,
			expectedLen: 3,
			expectedPoints: []struct {
				name   string
				tags   map[string]string
				fields map[string]interface{}
			}{
				{
					name:   "inverter-production-inv123",
					tags:   map[string]string{"serial": "inv123"},
					fields: map[string]interface{}{"P": 250},
				},
				{
					name:   "inverter-production-inv456",
					tags:   map[string]string{"serial": "inv456"},
					fields: map[string]interface{}{"P": 300},
				},
				{
					name:   "inverter-production-inv789",
					tags:   map[string]string{"serial": "inv789"},
					fields: map[string]interface{}{"P": 0},
				},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := extractInverterStats(tt.inverters, tt.ts)

			if len(got) != tt.expectedLen {
				t.Errorf("extractInverterStats() len = %v, want %v", len(got), tt.expectedLen)
			}

			if len(tt.expectedPoints) > 0 {
				for i, expectedPt := range tt.expectedPoints {
					if i >= len(got) {
						t.Errorf("Missing expected point: %s", expectedPt.name)
						continue
					}
					actualPt := got[i]
					expectedFullPoint := influxdb2.NewPointWithMeasurement(expectedPt.name)
					for k, v := range expectedPt.tags {
						expectedFullPoint.AddTag(k, v)
					}
					// Add common tags that are always expected
					expectedFullPoint.AddTag("source", cfg.SourceTag)
					expectedFullPoint.AddTag("measurement-type", "inverter")

					for k, v := range expectedPt.fields {
						expectedFullPoint.AddField(k, v)
					}
					expectedFullPoint.SetTime(tt.ts)

					// Use the more detailed comparePoints helper
					comparePoints(t, expectedFullPoint, actualPt, true)
				}
			}
		})
	}
}

func TestExtractProductionStats(t *testing.T) {
	cfg.SourceTag = "test-source"
	now := time.Now().Truncate(time.Second)

	tests := []struct {
		name          string
		prod          *envoy.ProductionResponse
		ts            time.Time
		expectedLen   int
		expectedNames []string          // For verifying a subset of generated points
		expectedTags  map[string]string // Common tags to check for the first point if any
	}{
		{
			name:        "Nil input",
			prod:        nil,
			ts:          now,
			expectedLen: 0,
		},
		{
			name: "Empty ProductionResponse",
			prod: &envoy.ProductionResponse{
				Production:  []envoy.Measurement{},
				Consumption: []envoy.Measurement{},
			},
			ts:          now,
			expectedLen: 0,
		},
		{
			name: "Production, Total Consumption, Net Consumption",
			prod: &envoy.ProductionResponse{
				Production: []envoy.Measurement{
					{
						MeasurementType: "production",
						Lines: []envoy.Line{
							{WNow: 100, ReactPwr: 10, ApprntPwr: 101, RmsCurrent: 1, RmsVoltage: 230},
						},
					},
				},
				Consumption: []envoy.Measurement{
					{
						MeasurementType: "total-consumption",
						Lines: []envoy.Line{
							{WNow: 200, ReactPwr: 20, ApprntPwr: 202, RmsCurrent: 2, RmsVoltage: 231},
						},
					},
					{
						MeasurementType: "net-consumption",
						Lines: []envoy.Line{
							{WNow: -100, ReactPwr: -10, ApprntPwr: -101, RmsCurrent: -1, RmsVoltage: 230},
						},
					},
				},
			},
			ts:            now,
			expectedLen:   3,
			expectedNames: []string{"production-line0", "consumption-line0", "net-line0"},
			expectedTags: map[string]string{
				"source":   "test-source",
				"line-idx": "0",
			},
		},
		{
			name: "Only production data",
			prod: &envoy.ProductionResponse{
				Production: []envoy.Measurement{
					{
						MeasurementType: "production",
						Lines: []envoy.Line{
							{WNow: 150, ReactPwr: 15, ApprntPwr: 151, RmsCurrent: 1.5, RmsVoltage: 230.5},
							{WNow: 160, ReactPwr: 16, ApprntPwr: 161, RmsCurrent: 1.6, RmsVoltage: 230.6},
						},
					},
				},
				Consumption: []envoy.Measurement{},
			},
			ts:            now,
			expectedLen:   2,
			expectedNames: []string{"production-line0", "production-line1"},
		},
		{
			name: "Unexpected MeasurementType",
			prod: &envoy.ProductionResponse{
				Production: []envoy.Measurement{
					{
						MeasurementType: "unknown-type", // This should be skipped
						Lines: []envoy.Line{
							{WNow: 300},
						},
					},
				},
				Consumption: []envoy.Measurement{
					{
						MeasurementType: "total-consumption",
						Lines: []envoy.Line{
							{WNow: 250},
						},
					},
				},
			},
			ts:            now,
			expectedLen:   1,
			expectedNames: []string{"consumption-line0"},
		},
		{
			name: "Production with no lines",
			prod: &envoy.ProductionResponse{
				Production: []envoy.Measurement{
					{
						MeasurementType: "production",
						Lines:           []envoy.Line{}, // Empty lines
					},
				},
				Consumption: []envoy.Measurement{
					{
						MeasurementType: "total-consumption",
						Lines: []envoy.Line{
							{WNow: 250},
						},
					},
				},
			},
			ts:            now,
			expectedLen:   1,
			expectedNames: []string{"consumption-line0"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Handle nil prod case explicitly for extractProductionStats
			var got []*influxdb2write.Point
			if tt.prod == nil {
				got = extractProductionStats(nil, tt.ts)
			} else {
				got = extractProductionStats(tt.prod, tt.ts)
			}

			if len(got) != tt.expectedLen {
				t.Errorf("extractProductionStats() len = %v, want %v", len(got), tt.expectedLen)
			}

			if len(got) > 0 && len(tt.expectedNames) > 0 {
				for i, name := range tt.expectedNames {
					if i < len(got) {
						if got[i].Name() != name {
							t.Errorf("extractProductionStats() point %d name = %s, want %s", i, got[i].Name(), name)
						}
						if !got[i].Time().Equal(tt.ts) {
							t.Errorf("extractProductionStats() point %d time = %v, want %v", i, got[i].Time(), tt.ts)
						}
						// Check common tags if specified
						if tt.expectedTags != nil && i == 0 { // Example: check first point's common tags
							for tagKey, tagVal := range tt.expectedTags {
								actualTag := findTag(got[i].TagList(), tagKey)
								if actualTag == nil || actualTag.Value != tagVal {
									t.Errorf("extractProductionStats() point %d tag '%s' = %v, want %v", i, tagKey, actualTag, tagVal)
								}
							}
						}
					} else {
						t.Errorf("extractProductionStats() expected more points than generated for name check. Expected name: %s", name)
					}
				}
			}
		})
	}
}

// MockEnvoyClient implements EnvoyClientInterface for testing
type MockEnvoyClient struct {
	CommCheckFunc           func() (*envoy.CommCheckResponse, error)
	ProductionFunc          func() (*envoy.ProductionResponse, error)
	InvertersFunc           func() (*[]envoy.Inverter, error)
	BatteriesFunc           func() (*[]envoy.Battery, error)
	InvalidateSessionFunc   func()
	invalidateSessionCalled bool
}

func (m *MockEnvoyClient) CommCheck() (*envoy.CommCheckResponse, error) {
	if m.CommCheckFunc != nil {
		return m.CommCheckFunc()
	}
	// Default behavior
	return &envoy.CommCheckResponse{}, nil
}

func (m *MockEnvoyClient) Production() (*envoy.ProductionResponse, error) {
	if m.ProductionFunc != nil {
		return m.ProductionFunc()
	}
	return &envoy.ProductionResponse{}, nil
}

func (m *MockEnvoyClient) Inverters() (*[]envoy.Inverter, error) {
	if m.InvertersFunc != nil {
		return m.InvertersFunc()
	}
	return &[]envoy.Inverter{}, nil
}

func (m *MockEnvoyClient) Batteries() (*[]envoy.Battery, error) {
	if m.BatteriesFunc != nil {
		return m.BatteriesFunc()
	}
	return &[]envoy.Battery{}, nil
}

func (m *MockEnvoyClient) InvalidateSession() {
	m.invalidateSessionCalled = true
	if m.InvalidateSessionFunc != nil {
		m.InvalidateSessionFunc()
	}
}

// MockInfluxWriter implements influxdb2write.WriteAPI for testing
type MockInfluxWriter struct {
	WritePointFunc  func(ctx context.Context, point ...*influxdb2write.Point) error
	PointsWritten   []*influxdb2write.Point
	WritePointError error
}

func (m *MockInfluxWriter) WritePoint(ctx context.Context, point ...*influxdb2write.Point) error {
	m.PointsWritten = append(m.PointsWritten, point...)
	if m.WritePointFunc != nil {
		return m.WritePointFunc(ctx, point...)
	}
	return m.WritePointError
}

func (m *MockInfluxWriter) EnableBatching() {
	return
}

func (w *MockInfluxWriter) WriteRecord(ctx context.Context, line ...string) error {
	return nil
}

func (m *MockInfluxWriter) Flush(ctx context.Context) error {
	// No-op for testing, unless specific flush behavior is needed
	return nil
}

func (m *MockInfluxWriter) Errors() <-chan error {
	// Return a nil or a closed channel if errors are not actively tested
	// For simplicity, returning a nil channel:
	return nil
}

// Helper to reset mocks if needed, or create new ones per test
func newMockEnvoyClient() *MockEnvoyClient {
	return &MockEnvoyClient{}
}

func newMockInfluxWriter() *MockInfluxWriter {
	return &MockInfluxWriter{
		PointsWritten: make([]*influxdb2write.Point, 0), // Ensure it's empty
	}
}

func TestLineToPoint(t *testing.T) {
	cfg.SourceTag = "test-source"           // Set a dummy source tag for testing
	now := time.Now().Truncate(time.Second) // Truncate for consistent comparison

	tests := []struct {
		name       string
		lineType   string
		line       envoy.Line
		idx        int
		ts         time.Time
		wantName   string
		wantTags   map[string]string
		wantFields map[string]interface{}
	}{
		{
			name:     "Valid production line",
			lineType: "production",
			line: envoy.Line{
				WNow:       100.5,
				ReactPwr:   10.1,
				ApprntPwr:  101.0,
				RmsCurrent: 1.5,
				RmsVoltage: 230.2,
			},
			idx:      0,
			ts:       now,
			wantName: "production-line0",
			wantTags: map[string]string{
				"source":           "test-source",
				"measurement-type": "production",
				"line-idx":         "0",
			},
			wantFields: map[string]interface{}{
				"P":     100.5,
				"Q":     10.1,
				"S":     101.0,
				"I_rms": 1.5,
				"V_rms": 230.2,
			},
		},
		{
			name:     "Valid consumption line with different index",
			lineType: "consumption",
			line: envoy.Line{
				WNow:       200.0,
				ReactPwr:   20.5,
				ApprntPwr:  205.0,
				RmsCurrent: 2.5,
				RmsVoltage: 230.5,
			},
			idx:      1,
			ts:       now,
			wantName: "consumption-line1",
			wantTags: map[string]string{
				"source":           "test-source",
				"measurement-type": "consumption",
				"line-idx":         "1",
			},
			wantFields: map[string]interface{}{
				"P":     200.0,
				"Q":     20.5,
				"S":     205.0,
				"I_rms": 2.5,
				"V_rms": 230.5,
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := lineToPoint(tt.lineType, tt.line, tt.idx, tt.ts)

			if got.Name() != tt.wantName {
				t.Errorf("lineToPoint() name = %v, want %v", got.Name(), tt.wantName)
			}

			if !got.Time().Equal(tt.ts) {
				t.Errorf("lineToPoint() time = %v, want %v", got.Time(), tt.ts)
			}

			for key, wantValue := range tt.wantTags {
				actualTag := findTag(got.TagList(), key)
				if actualTag == nil {
					t.Errorf("lineToPoint() tag key '%s' not found", key)
					continue
				}
				if actualTag.Value != wantValue {
					t.Errorf("lineToPoint() tag key '%s' value = %v, want %v", key, actualTag.Value, wantValue)
				}
			}
			if len(got.TagList()) != len(tt.wantTags) {
				t.Errorf("lineToPoint() unexpected tags. Got %d, want %d. Got: %v", len(got.TagList()), len(tt.wantTags), got.TagList())
			}

			for key, wantValue := range tt.wantFields {
				actualField := findField(got.FieldList(), key)
				if actualField == nil {
					t.Errorf("lineToPoint() field key '%s' not found", key)
					continue
				}
				// Convert to float64 for comparison, as InfluxDB client might store numbers as float64
				var gotValueFloat float64
				switch v := actualField.Value.(type) {
				case float64:
					gotValueFloat = v
				case float32:
					gotValueFloat = float64(v)
				case int:
					gotValueFloat = float64(v)
				case int64:
					gotValueFloat = float64(v)
				default:
					t.Errorf("lineToPoint() field key '%s' has unhandled type %T", key, actualField.Value)
				}

				if gotValueFloat != wantValue.(float64) {
					t.Errorf("lineToPoint() field key '%s' value = %v, want %v", key, gotValueFloat, wantValue)
				}
			}
			if len(got.FieldList()) != len(tt.wantFields) {
				t.Errorf("lineToPoint() unexpected fields. Got %d, want %d. Got: %v", len(got.FieldList()), len(tt.wantFields), got.FieldList())
			}
		})
	}
}
