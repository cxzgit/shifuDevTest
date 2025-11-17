# Build stage
FROM golang:1.22-alpine AS builder

WORKDIR /app

# Copy driver code and dependency file first
COPY . .

# Install prerequisites for fetching Go modules
RUN apk add --no-cache git

# Tidy modules (no go.sum included)
RUN go mod tidy

# Build a static binary
RUN CGO_ENABLED=0 go build -o /bin/driver .

# Runtime stage
FROM alpine:3.20

# Install certificates for networking
RUN apk add --no-cache ca-certificates

# Create non-root user
RUN addgroup -S app && adduser -S -G app app

# Copy built binary
COPY --from=builder /bin/driver /usr/local/bin/driver
RUN chmod +x /usr/local/bin/driver && chown app:app /usr/local/bin/driver

USER app

ENTRYPOINT ["/usr/local/bin/driver"]
