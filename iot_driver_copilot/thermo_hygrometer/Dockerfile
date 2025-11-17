FROM golang:1.22-alpine AS builder
WORKDIR /app

# Copy dependency manifest and download/tidy modules
COPY go.mod ./
RUN go mod tidy

# Copy source code
COPY . .

# Build a static binary for Linux
RUN CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -o /usr/local/bin/driver ./main.go

# Use a minimal runtime image
FROM alpine:3.20
RUN apk add --no-cache ca-certificates && adduser -D -u 10001 appuser
COPY --from=builder /usr/local/bin/driver /usr/local/bin/driver
USER 10001
ENTRYPOINT ["/usr/local/bin/driver"]
