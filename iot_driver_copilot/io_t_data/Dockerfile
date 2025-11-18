# Build stage
FROM golang:1.23-alpine AS builder

WORKDIR /app

# Copy dependency file first for better caching
COPY go.mod ./

# Copy source code
COPY . .

# Tidy modules (required for Go builds without go.sum)
RUN go mod tidy

# Build statically linked binary
RUN CGO_ENABLED=0 GOOS=linux go build -o /driver -trimpath -ldflags "-s -w" -buildvcs=false .

# Runtime stage
FROM alpine:3.19

# Install minimal runtime prerequisites
RUN apk add --no-cache ca-certificates tzdata

# Copy the built binary
COPY --from=builder /driver /usr/local/bin/driver

# Run the driver
ENTRYPOINT ["/usr/local/bin/driver"]
