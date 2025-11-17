FROM golang:1.22-alpine AS builder
RUN apk add --no-cache ca-certificates git
WORKDIR /app
COPY . .
ENV CGO_ENABLED=0
RUN go mod tidy
RUN go build -trimpath -ldflags="-s -w" -o /usr/local/bin/driver ./main.go

FROM alpine:3.20
RUN apk add --no-cache ca-certificates && addgroup -S app && adduser -S -G app app
COPY --from=builder /usr/local/bin/driver /usr/local/bin/driver
USER app
ENTRYPOINT ["/usr/local/bin/driver"]
