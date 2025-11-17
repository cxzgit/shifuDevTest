FROM golang:1.22-alpine AS builder
WORKDIR /app
COPY . .
RUN go mod tidy
RUN CGO_ENABLED=0 go build -trimpath -ldflags "-s -w" -o driver ./...

FROM alpine:3.20
RUN apk add --no-cache ca-certificates
COPY --from=builder /app/driver /usr/local/bin/driver
ENTRYPOINT ["/usr/local/bin/driver"]
