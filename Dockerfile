# Build stage
FROM golang:1.25.1-alpine AS builder

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
FROM alpine:latest

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
