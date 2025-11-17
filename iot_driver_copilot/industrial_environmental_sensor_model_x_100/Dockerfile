# Builder stage
FROM golang:1.22-alpine AS builder
WORKDIR /app

# Copy dependency file and source code
COPY go.mod .
COPY *.go ./

# Install modules
RUN go mod tidy

# Build the binary
RUN CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -o driver .

# Runtime stage
FROM alpine:3.19
RUN apk add --no-cache ca-certificates && adduser -D -u 10001 appuser
WORKDIR /app
COPY --from=builder /app/driver /app/driver
USER appuser
ENTRYPOINT ["./driver"]
