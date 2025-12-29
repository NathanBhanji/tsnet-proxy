# Build stage - use target platform for native compilation with CGO
FROM golang:1.25-alpine AS builder

WORKDIR /app

# Install build dependencies (including CGO requirements)
RUN apk add --no-cache git ca-certificates gcc musl-dev

# Copy go mod files
COPY go.mod go.sum ./
RUN go mod download

# Copy source code
COPY . .

# Build the binary with CGO enabled (native compilation for each platform)
RUN CGO_ENABLED=1 go build \
    -ldflags='-w -s' \
    -o tsnet-proxy \
    ./cmd/tsnet-proxy

# Runtime stage
FROM alpine:latest

# Install runtime dependencies (including libc for CGO-built binary)
RUN apk --no-cache add ca-certificates libc6-compat

WORKDIR /app

# Copy binary from builder
COPY --from=builder /app/tsnet-proxy .

# Copy default config
COPY configs/services.yaml /app/configs/services.yaml

# Create data directory for tsnet state
RUN mkdir -p /data/tsnet

# Expose ports
# 8080: Management UI
# 9090: Prometheus metrics
EXPOSE 8080 9090

ENTRYPOINT ["/app/tsnet-proxy"]
CMD ["--config", "/app/configs/services.yaml"]
