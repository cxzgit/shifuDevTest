# syntax=docker/dockerfile:1

FROM golang:1.22-alpine AS build
WORKDIR /app

# Copy dependency manifest and source code
COPY go.mod ./
COPY . .

# Tidy modules since go.sum isn't provided
RUN go mod tidy

# Build a static binary
RUN --mount=type=cache,target=/root/.cache/go-build \
    CGO_ENABLED=0 go build -trimpath -ldflags "-s -w" -o /app/driver ./

FROM alpine:3.20 AS runtime
# Create non-root user
RUN addgroup -S app && adduser -S -G app app
WORKDIR /app

COPY --from=build /app/driver /usr/local/bin/driver

USER app
ENTRYPOINT ["/usr/local/bin/driver"]
