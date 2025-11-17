# Build stage
FROM golang:1.22-alpine AS builder
WORKDIR /app

# Copy driver code and dependency file first
COPY . .

# Install dependencies using go.mod
RUN go mod tidy

# Build the binary
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /bin/driver ./

# Runtime stage
FROM alpine:3.20
RUN apk add --no-cache ca-certificates
COPY --from=builder /bin/driver /usr/local/bin/driver

ENTRYPOINT ["/usr/local/bin/driver"]
