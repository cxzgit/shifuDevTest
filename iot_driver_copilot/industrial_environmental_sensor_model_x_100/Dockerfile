FROM golang:1.22-alpine AS builder
WORKDIR /app

# Copy dependency file first and tidy modules
COPY go.mod ./
RUN go mod tidy

# Copy source code and build
COPY . .
RUN CGO_ENABLED=0 GOOS=linux go build -o driver

# Final minimal runtime image
FROM alpine:3.19
WORKDIR /app
RUN apk add --no-cache ca-certificates && addgroup -S app && adduser -S app -G app
COPY --from=builder /app/driver /app/driver
USER app
CMD ["./driver"]