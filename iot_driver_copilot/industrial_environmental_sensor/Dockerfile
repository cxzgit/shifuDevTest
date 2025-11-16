# Multi-stage build for a minimal production image
FROM golang:1.22-alpine AS builder

# Install prerequisites (e.g., git for fetching modules)
RUN apk add --no-cache git

WORKDIR /app

# Copy driver source code and dependency file first
COPY . .

# Prepare and verify module dependencies
RUN go mod tidy

# Build static binary for portability
ENV CGO_ENABLED=0
RUN go build -o driver .

# Final runtime image
FROM alpine:3.20

# Create non-root user for security
RUN adduser -D -u 10001 appuser
WORKDIR /app

# Copy built binary
COPY --from=builder /app/driver /usr/local/bin/driver

USER appuser
ENTRYPOINT ["/usr/local/bin/driver"]
