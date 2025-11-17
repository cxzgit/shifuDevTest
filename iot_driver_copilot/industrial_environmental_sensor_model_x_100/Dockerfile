# Builder stage
FROM golang:1.22-alpine AS builder
WORKDIR /app

# Install prerequisites
RUN apk add --no-cache git

# Copy dependency file and source code
COPY go.mod ./
COPY . .

# Install dependencies
RUN go mod tidy

# Build static binary
ENV CGO_ENABLED=0
RUN go build -trimpath -ldflags="-s -w" -o /app/driver ./main.go

# Runtime stage
FROM alpine:3.20
RUN apk add --no-cache ca-certificates
WORKDIR /app

# Create non-root user
RUN addgroup -S app && adduser -S -G app app

# Copy built binary
COPY --from=builder /app/driver /usr/local/bin/driver

USER app
ENTRYPOINT ["/usr/local/bin/driver"]
