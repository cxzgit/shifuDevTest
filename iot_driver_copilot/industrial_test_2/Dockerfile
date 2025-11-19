# Build stage
FROM alpine:3.19 AS build
WORKDIR /app
# Copy source code and dependency file first
COPY . /app
# Install build prerequisites
RUN apk add --no-cache build-base cmake
# Build using CMake
RUN cmake -S . -B build -DCMAKE_BUILD_TYPE=Release && \
    cmake --build build -j

# Runtime stage
FROM alpine:3.19
# Install required runtime libraries
RUN apk add --no-cache libstdc++ libgcc
WORKDIR /app
# Copy compiled binary
COPY --from=build /app/build/driver /usr/local/bin/driver
# Expose default HTTP port (configurable via env)
EXPOSE 8000
# Run the driver
ENTRYPOINT ["/usr/local/bin/driver"]
