# Build stage
FROM golang:1.22-alpine AS builder

WORKDIR /app

# Copy dependency file and source code
COPY go.mod ./
COPY . .

# Tidy modules (preferred over download when no go.sum is provided)
RUN go mod tidy

# Build static binary
RUN CGO_ENABLED=0 go build -o /usr/local/bin/driver .

# Runtime stage
FROM alpine:3.20

# Optional prerequisites for production (e.g., SSL certs)
RUN apk add --no-cache ca-certificates && update-ca-certificates

# Create non-root user
RUN addgroup -S app && adduser -S -G app app

# Copy built binary
COPY --from=builder /usr/local/bin/driver /usr/local/bin/driver

USER app
ENTRYPOINT ["/usr/local/bin/driver"]
