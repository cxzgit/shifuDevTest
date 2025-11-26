FROM golang:1.22-alpine AS builder

WORKDIR /app

# Copy dependency file and source code into the working directory
COPY go.mod .
COPY main.go .

# Install Go module dependencies
RUN go mod tidy

# Build the driver binary
RUN CGO_ENABLED=0 go build -o driver .

FROM alpine:3.20

# Install runtime prerequisites
RUN apk add --no-cache ca-certificates && update-ca-certificates

# Create a non-root user
RUN addgroup -S app && adduser -S -G app app

WORKDIR /app

# Copy the built binary from the builder stage
COPY --from=builder /app/driver /usr/local/bin/driver

USER app

ENTRYPOINT ["/usr/local/bin/driver"]
