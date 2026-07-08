# Build stage
FROM golang:1.26.5-alpine@sha256:99e12cfb19b753915f9b9fdc5a99f1869a24a69d3a0955832d5702e7fa68f1be AS builder

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
