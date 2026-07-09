# Build stage
FROM golang:1.26.5-alpine@sha256:0178a641fbb4858c5f1b48e34bdaabe0350a330a1b1149aabd498d0699ff5fb2 AS builder

WORKDIR /app

# Install build dependencies
RUN apk add --no-cache git

# Download dependencies
COPY go.mod go.sum ./
RUN go mod download

# Copy source code
COPY . .

# Build the binary
RUN CGO_ENABLED=0 GOOS=linux go build -o envoy-exporter .

# Final stage
FROM alpine:latest@sha256:28bd5fe8b56d1bd048e5babf5b10710ebe0bae67db86916198a6eec434943f8b

# Install runtime dependencies (CA certificates for HTTPS)
RUN apk add --no-cache ca-certificates tzdata

# Create a non-privileged user
RUN adduser -D envoy-exporter
USER envoy-exporter

WORKDIR /app

# Copy binary from builder
COPY --from=builder /app/envoy-exporter /usr/bin/envoy-exporter

# Default config path
ENV CONFIG_PATH=/etc/envoy-exporter/envoy.yaml

# Expose expvar port
EXPOSE 6666

# Run the exporter
ENTRYPOINT ["/usr/bin/envoy-exporter"]
CMD ["-config", "/etc/envoy-exporter/envoy.yaml"]
