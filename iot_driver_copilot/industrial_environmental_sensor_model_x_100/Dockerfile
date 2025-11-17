# Builder stage
FROM golang:1.22-alpine AS builder

# Install prerequisites for fetching modules
RUN apk add --no-cache git

# Set working directory and copy source
WORKDIR /app
COPY . .

# Prepare and vendor dependencies
RUN go mod tidy

# Build static binary for Linux
ARG TARGETOS
ARG TARGETARCH
RUN CGO_ENABLED=0 GOOS=${TARGETOS:-linux} GOARCH=${TARGETARCH:-amd64} \
    go build -o /bin/driver -trimpath -ldflags "-s -w" ./

# Final runtime stage
FROM alpine:3.20
RUN apk add --no-cache ca-certificates \
 && addgroup -S app && adduser -S -G app app

# Copy built binary
COPY --from=builder /bin/driver /usr/local/bin/driver

USER app
WORKDIR /home/app

ENTRYPOINT ["/usr/local/bin/driver"]