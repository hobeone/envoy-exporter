module github.com/hobeone/envoy-exporter

go 1.25.0

toolchain go1.25.1

require (
	github.com/golang-jwt/jwt/v4 v4.5.2
	github.com/hobeone/enphase-gateway v1.1.1
	github.com/influxdata/influxdb-client-go/v2 v2.14.0
	github.com/stretchr/testify v1.11.1
	gopkg.in/yaml.v3 v3.0.1
)

// replace github.com/hobeone/enphase-gateway => ../enphase-gateway

require (
	github.com/apapsch/go-jsonmerge/v2 v2.0.0 // indirect
	github.com/davecgh/go-spew v1.1.2-0.20180830191138-d8f796af33cc // indirect
	github.com/google/uuid v1.6.0 // indirect
	github.com/influxdata/line-protocol v0.0.0-20210922203350-b1ad95c89adf // indirect
	github.com/oapi-codegen/runtime v1.4.0 // indirect
	github.com/pmezard/go-difflib v1.0.0 // indirect
	golang.org/x/net v0.53.0 // indirect
)
