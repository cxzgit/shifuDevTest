# Multi-stage build for Go driver
FROM golang:1.22-alpine AS builder

# Create working directory and copy source + dependency manifest first
WORKDIR /app
COPY . .

# Tidy modules (since go.sum isn't provided)
RUN go mod tidy

# Build statically for minimal runtime image
RUN CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -o /app/driver .

# Runtime stage
FROM alpine:3.20
RUN apk --no-cache add ca-certificates && \
    addgroup -S app && adduser -S app -G app

WORKDIR /app
COPY --from=builder /app/driver /app/driver

USER app
ENTRYPOINT ["/app/driver"]