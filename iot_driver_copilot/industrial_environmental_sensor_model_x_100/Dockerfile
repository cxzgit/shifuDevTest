# Builder stage
FROM golang:1.22-alpine AS builder
WORKDIR /app

# Copy dependency file and driver code to working directory first
COPY go.mod .
COPY . .

# Install prerequisites not covered by go.mod
RUN apk add --no-cache git

# Use go mod tidy since go.sum is not provided
RUN go mod tidy

# Build a statically linked binary
RUN CGO_ENABLED=0 go build -o /app/driver -trimpath -ldflags="-s -w" .

# Runtime stage
FROM alpine:3.20
RUN adduser -D -H -u 10001 appuser \
    && apk add --no-cache ca-certificates \
    && update-ca-certificates

COPY --from=builder /app/driver /usr/local/bin/driver

USER 10001
ENTRYPOINT ["/usr/local/bin/driver"]
