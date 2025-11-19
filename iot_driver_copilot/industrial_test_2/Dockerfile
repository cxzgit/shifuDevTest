# syntax=docker/dockerfile:1

FROM alpine:3.20 AS builder

WORKDIR /app

# Install build prerequisites
RUN apk add --no-cache build-base

# Copy source files and build manifest
COPY . /app

# Build the driver
RUN make

# Runtime image
FROM alpine:3.20

# Create a non-root user for security
RUN adduser -D -u 10001 appuser

# Copy the built binary
COPY --from=builder /app/driver /usr/local/bin/driver

# Switch to the non-root user
USER appuser

# Expose default port (actual port is set via HTTP_PORT env)
EXPOSE 8000

# Run the driver
ENTRYPOINT ["/usr/local/bin/driver"]
