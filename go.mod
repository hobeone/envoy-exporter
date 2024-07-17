module github.com/hobeone/envoy-exporter

go 1.22.3

require (
	github.com/davecgh/go-spew v1.1.2-0.20180830191138-d8f796af33cc
	github.com/influxdata/influxdb-client-go/v2 v2.13.0
	github.com/loafoe/go-envoy v0.0.15
	github.com/sirupsen/logrus v1.9.3
	gopkg.in/yaml.v3 v3.0.1
)

require (
	github.com/apapsch/go-jsonmerge/v2 v2.0.0 // indirect
	github.com/golang-jwt/jwt/v4 v4.5.0 // indirect
	github.com/google/uuid v1.6.0 // indirect
	github.com/hashicorp/mdns v1.0.5 // indirect
	github.com/influxdata/line-protocol v0.0.0-20210922203350-b1ad95c89adf // indirect
	github.com/kr/pretty v0.3.1 // indirect
	github.com/miekg/dns v1.1.61 // indirect
	github.com/oapi-codegen/runtime v1.1.1 // indirect
	golang.org/x/mod v0.19.0 // indirect
	golang.org/x/net v0.27.0 // indirect
	golang.org/x/sync v0.7.0 // indirect
	golang.org/x/sys v0.22.0 // indirect
	golang.org/x/tools v0.23.0 // indirect
)

replace github.com/loafoe/go-envoy v0.0.15 => ../go-envoy
