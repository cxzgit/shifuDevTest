FROM golang:1.22-alpine AS builder
WORKDIR /app

# Copy dependency manifest and driver code first
COPY go.mod .
COPY main.go .

# Install and tidy modules
RUN go mod tidy

# Build static binary for minimal runtime image
RUN CGO_ENABLED=0 GOOS=linux go build -o driver

FROM alpine:3.19
# Install runtime prerequisites and create non-root user
RUN apk add --no-cache ca-certificates && adduser -D -u 10001 appuser
WORKDIR /app

# Copy built binary
COPY --from=builder /app/driver /usr/local/bin/driver

USER appuser
ENTRYPOINT ["/usr/local/bin/driver"]