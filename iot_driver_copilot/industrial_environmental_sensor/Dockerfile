# Multi-stage build for Go IoT driver
FROM golang:1.22-alpine AS builder

# Create working directory
WORKDIR /app

# Copy source code and dependency file first
COPY . .

# Resolve and tidy modules (no go.sum included)
RUN go mod tidy

# Build statically linked binary for minimal runtime image
RUN CGO_ENABLED=0 go build -ldflags="-s -w" -o /bin/driver ./main.go

# Final minimal runtime image
FROM alpine:3.20

# Install runtime prerequisites
RUN apk --no-cache add ca-certificates && adduser -S -D -H appuser

WORKDIR /app

# Copy built binary from builder stage
COPY --from=builder /bin/driver /usr/local/bin/driver

# Run as non-root user
USER appuser

ENTRYPOINT ["/usr/local/bin/driver"]
