# Multi-stage build for Go Modbus RTU driver

FROM golang:1.20-alpine AS builder
WORKDIR /app

# Copy dependency file and source code before installing dependencies
COPY go.mod .
COPY *.go .

# Fetch dependencies (no go.sum provided, so use tidy)
RUN go mod tidy

# Build a static binary for minimal runtime footprint
RUN CGO_ENABLED=0 go build -o driver -trimpath -ldflags="-s -w" .

FROM alpine:3.19
WORKDIR /app

# Copy built binary
COPY --from=builder /app/driver /app/driver

# Expose HTTP port
EXPOSE 8080

# Run the driver
ENTRYPOINT ["/app/driver"]
