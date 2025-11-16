FROM golang:1.22-alpine AS builder
WORKDIR /app

# Copy dependency file and driver source code
COPY go.mod ./
COPY *.go ./

# Install prerequisites not covered by go.mod (e.g., git for fetching modules)
RUN apk add --no-cache git

# Use go mod tidy to resolve and write go.sum
RUN go mod tidy

# Build static binary for smaller runtime image
RUN CGO_ENABLED=0 go build -o /out/driver -trimpath -ldflags "-s -w" .

# Runtime image
FROM alpine:3.20
RUN apk add --no-cache ca-certificates && update-ca-certificates

# Create non-root user for security
RUN adduser -D -u 10001 appuser

# Copy compiled binary
COPY --from=builder /out/driver /usr/local/bin/driver

USER appuser
WORKDIR /work
ENTRYPOINT ["/usr/local/bin/driver"]
