# syntax=docker/dockerfile:1

# Build stage
FROM golang:1.22-alpine AS builder
WORKDIR /app

# Copy driver code and dependency file first
COPY . .

# Install Go module dependencies
RUN go mod tidy

# Build the binary
RUN CGO_ENABLED=0 GOOS=linux go build -o driver .

# Runtime stage
FROM alpine:3.20
RUN apk --no-cache add ca-certificates && \
    adduser -D -u 10001 appuser
WORKDIR /app
COPY --from=builder /app/driver /usr/local/bin/driver
USER appuser
ENTRYPOINT ["/usr/local/bin/driver"]
