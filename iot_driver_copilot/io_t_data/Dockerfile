# Multi-stage build for Go driver
FROM golang:1.22-alpine AS builder

WORKDIR /app

# Copy source code and dependency file
COPY . .

# Install dependencies
RUN go mod tidy

# Build a statically linked binary
ENV CGO_ENABLED=0
RUN go build -trimpath -ldflags="-s -w" -o /usr/local/bin/driver .

# Runtime image
FROM alpine:3.20

COPY --from=builder /usr/local/bin/driver /usr/local/bin/driver

ENTRYPOINT ["/usr/local/bin/driver"]
