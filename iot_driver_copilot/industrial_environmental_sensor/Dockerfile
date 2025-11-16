# Build stage
FROM golang:1.22-alpine AS build
WORKDIR /app

# Copy dependency file and source code
COPY go.mod ./
COPY . .

# Install dependencies (use tidy as go.sum is not included)
RUN go mod tidy

# Build the binary
RUN CGO_ENABLED=0 GOFLAGS="-buildvcs=false" go build -trimpath -ldflags="-s -w" -o /out/driver ./

# Runtime stage
FROM alpine:3.20
RUN apk add --no-cache ca-certificates tzdata && \
    addgroup -S app && adduser -S -G app app

COPY --from=build /out/driver /usr/local/bin/driver
USER app
ENTRYPOINT ["/usr/local/bin/driver"]
