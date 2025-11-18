FROM golang:1.22-alpine AS builder

WORKDIR /app
COPY go.mod ./
RUN go mod tidy

COPY . .
RUN CGO_ENABLED=0 go build -o /bin/driver -ldflags="-s -w" .

FROM alpine:3.20
RUN apk add --no-cache ca-certificates
COPY --from=builder /bin/driver /usr/local/bin/driver
USER 10001
ENTRYPOINT ["/usr/local/bin/driver"]
