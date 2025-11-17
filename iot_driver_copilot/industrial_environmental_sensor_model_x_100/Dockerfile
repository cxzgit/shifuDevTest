# Build stage
FROM golang:1.22-alpine AS builder

WORKDIR /app

# Copy source code and dependency file first
COPY . .

# Tidy modules since go.sum is not included
RUN go mod tidy

# Build statically linked binary
RUN CGO_ENABLED=0 GOOS=linux go build -o /app/driver .

# Runtime stage
FROM alpine:3.20

# Install runtime prerequisites
RUN apk add --no-cache ca-certificates && adduser -D -u 10001 appuser

WORKDIR /home/appuser

COPY --from=builder /app/driver /usr/local/bin/driver

USER appuser

CMD ["/usr/local/bin/driver"]
