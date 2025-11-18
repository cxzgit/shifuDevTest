FROM golang:1.22-alpine AS builder
WORKDIR /app

# Copy dependency file and source code
COPY go.mod ./
COPY main.go ./

# Ensure module files are up to date
RUN go mod tidy

# Build a static binary for minimal runtime image
RUN CGO_ENABLED=0 go build -trimpath -o driver .

# Runtime stage
FROM alpine:3.20
# Create non-root user for security
RUN adduser -D -g "" appuser
COPY --from=builder /app/driver /usr/local/bin/driver
USER appuser
ENTRYPOINT ["/usr/local/bin/driver"]
