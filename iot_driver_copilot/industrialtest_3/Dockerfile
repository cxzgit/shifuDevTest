# syntax=docker/dockerfile:1

FROM golang:1.20-alpine AS builder
RUN apk add --no-cache git
WORKDIR /app

# Copy driver code and dependency file before installing deps
COPY . .

# Resolve dependencies
RUN go mod tidy

# Build static binary
ARG TARGETOS=linux
ARG TARGETARCH
RUN CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=${TARGETARCH:-amd64} go build -trimpath -ldflags="-s -w" -o /usr/local/bin/driver .

FROM alpine:3.19
WORKDIR /app
COPY --from=builder /usr/local/bin/driver /usr/local/bin/driver
EXPOSE 8080
ENTRYPOINT ["/usr/local/bin/driver"]