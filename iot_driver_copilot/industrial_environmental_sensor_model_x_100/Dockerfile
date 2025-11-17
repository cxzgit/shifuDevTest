FROM golang:1.22-alpine AS builder
WORKDIR /app

# Install prerequisites for fetching modules
RUN apk add --no-cache git ca-certificates

# Copy source code and dependency file first
COPY . .

# Resolve and tidy Go module dependencies
RUN go mod tidy

# Build static binary
RUN CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath -ldflags="-s -w" -o /app/driver ./

# Runtime image
FROM alpine:3.20
WORKDIR /app

RUN apk add --no-cache ca-certificates && adduser -D -u 10001 appuser

COPY --from=builder /app/driver /usr/local/bin/driver

USER appuser
ENTRYPOINT ["/usr/local/bin/driver"]
