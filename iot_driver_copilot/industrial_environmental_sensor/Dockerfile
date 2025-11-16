# Build stage
FROM golang:1.22-alpine AS builder
WORKDIR /app

# Copy driver code and dependency file first
COPY . .

# If the provided code is in a combined file, extract valid Go code and remove accidental Dockerfile lines
RUN if [ -f combined_driver_code ]; then sed '/^FROM/d' combined_driver_code > main.go; fi

# Install build prerequisites
RUN apk add --no-cache git

# Use go.mod to resolve dependencies
RUN go mod tidy

# Build static binary for Linux
RUN CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath -ldflags="-s -w" -o driver .

# Runtime stage
FROM alpine:3.20
RUN apk add --no-cache ca-certificates && adduser -D -H -u 10001 appuser
WORKDIR /app
COPY --from=builder /app/driver /app/driver
USER appuser
ENTRYPOINT ["/app/driver"]
