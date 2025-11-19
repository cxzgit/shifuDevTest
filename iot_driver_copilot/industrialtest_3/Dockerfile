# Multi-stage build for Go Modbus RTU → HTTP driver
FROM golang:1.20-alpine AS builder

WORKDIR /app

# Copy dependency manifest and source code first
COPY go.mod ./
COPY *.go ./

# Install tools required to fetch Go modules
RUN apk add --no-cache git

# Resolve and tidy dependencies
RUN go mod tidy

# Build static binary
RUN CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -o /app/driver -trimpath -ldflags "-s -w" .

# Runtime image
FROM alpine:3.19
WORKDIR /app

# Copy built binary
COPY --from=builder /app/driver /app/driver

# Expose HTTP port
EXPOSE 8080

# Run the driver
ENTRYPOINT ["/app/driver"]
