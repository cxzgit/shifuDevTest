# Multi-stage build for a minimal production image
FROM golang:1.22-alpine AS builder

WORKDIR /app

# Copy dependency file and driver code first
COPY go.mod .
COPY . .

# Use go mod tidy to fetch dependencies since go.sum is not included
RUN go mod tidy

# Build a statically linked binary
ENV CGO_ENABLED=0 GOOS=linux
RUN go build -trimpath -ldflags="-s -w" -o driver

FROM alpine:3.20 AS runtime

# Install runtime prerequisites
RUN apk add --no-cache ca-certificates

WORKDIR /app

# Create a non-root user for security
RUN addgroup -S app && adduser -S -G app app

# Copy the compiled binary
COPY --from=builder /app/driver /usr/local/bin/driver

USER app

ENTRYPOINT ["driver"]
