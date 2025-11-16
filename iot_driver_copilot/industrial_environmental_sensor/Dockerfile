# Multi-stage build for Go IoT driver
FROM golang:1.22-alpine AS builder

WORKDIR /app

# Copy dependency manifest and source code before installing dependencies
COPY go.mod .
COPY . .

# Tidy and fetch dependencies (even if none, ensures module files are consistent)
RUN go mod tidy

# Build static binary for minimal runtime image
RUN CGO_ENABLED=0 go build -o driver -trimpath -ldflags "-s -w" .

# Final minimal runtime image
FROM alpine:3.20

# Create non-root user
RUN adduser -D -u 10001 appuser

# Copy the built binary
COPY --from=builder /app/driver /usr/local/bin/driver

USER appuser
ENTRYPOINT ["/usr/local/bin/driver"]
