# Multi-stage Dockerfile for Go driver

FROM golang:1.22-alpine AS builder
WORKDIR /app

# Copy source and dependency manifest before installing deps
COPY . .

# Install prerequisites for fetching modules (if any)
RUN apk add --no-cache git ca-certificates

# Tidy modules (preferred since go.sum is not provided)
RUN go mod tidy

# Build static binary
ENV CGO_ENABLED=0 GOOS=linux
RUN go build -trimpath -ldflags "-s -w" -o /app/bin/driver .

# Final runtime image
FROM alpine:3.19
RUN adduser -D -u 10001 appuser
COPY --from=builder /app/bin/driver /usr/local/bin/driver
USER appuser
ENTRYPOINT ["/usr/local/bin/driver"]
