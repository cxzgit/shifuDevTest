# Builder stage
FROM alpine:3.20 AS builder
WORKDIR /app

# Copy source and build manifest first
COPY driver.cpp Makefile ./

# Install build prerequisites
RUN apk add --no-cache build-base

# Build the driver
RUN make

# Runtime stage
FROM alpine:3.20
WORKDIR /app

# Install runtime C++ standard library
RUN apk add --no-cache libstdc++

# Copy compiled binary
COPY --from=builder /app/driver /usr/local/bin/driver

# Default HTTP port (can be overridden with HTTP_PORT env)
EXPOSE 8080

# Run the driver
CMD ["/usr/local/bin/driver"]
