# Multi-stage build for C++17 Modbus RTU HTTP driver

# Builder stage
FROM alpine:3.20 AS builder
WORKDIR /app
# Copy source and build files first
COPY . /app
# Install build prerequisites
RUN apk add --no-cache build-base
# Build the driver using the generated Makefile
RUN make

# Runtime stage
FROM alpine:3.20
WORKDIR /app
# Install runtime prerequisites (C++ stdlib)
RUN apk add --no-cache libstdc++
# Copy built binary
COPY --from=builder /app/driver /app/driver

# Default environment values (can be overridden at runtime)
ENV HTTP_HOST=0.0.0.0 \
    HTTP_PORT=8080 \
    SERIAL_PORT=/dev/ttyUSB0 \
    SERIAL_BAUD=9600 \
    MODBUS_SLAVE_ID=1 \
    REG_TEMP_ADDR=0 \
    REG_HUM_ADDR=1 \
    REG_CO2_ADDR=2 \
    TEMP_SCALE=1 \
    HUM_SCALE=1 \
    CO2_SCALE=1 \
    POLL_INTERVAL_MS=1000 \
    READ_TIMEOUT_MS=500 \
    BACKOFF_INITIAL_MS=500 \
    BACKOFF_MAX_MS=10000

EXPOSE 8080

ENTRYPOINT ["/app/driver"]
