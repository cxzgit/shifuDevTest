# Multi-stage build for a small production image
FROM alpine:3.19 AS build

WORKDIR /usr/src/app
# Copy source and dependency file first
COPY Makefile driver.cpp config.cpp serial_port.cpp modbus_rtu.cpp http_server.cpp ./

# Install build prerequisites
RUN apk add --no-cache build-base

# Build the driver using the Makefile
RUN make

# Final runtime image
FROM alpine:3.19

# Install C++ runtime
RUN apk add --no-cache libstdc++

WORKDIR /app
COPY --from=build /usr/src/app/driver /usr/local/bin/driver

# Typical HTTP port (actual port is defined via env HTTP_PORT)
EXPOSE 8000

# The driver requires environment variables to be provided at runtime
ENTRYPOINT ["/usr/local/bin/driver"]
