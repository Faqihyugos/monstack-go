# Build stage
FROM golang:1.23-alpine AS builder

RUN apk add --no-cache make git

WORKDIR /app
COPY go.mod go.sum ./
RUN go mod download

COPY . .
RUN make build

# Runtime stage
FROM alpine:3.20

RUN apk add --no-cache ca-certificates tzdata && \
    adduser -D -u 1000 monstack

COPY --from=builder /app/bin/monstack /usr/local/bin/
COPY --from=builder /app/configs/examples/ /etc/monstack/

RUN mkdir -p /var/lib/monstack /var/log/monstack && \
    chown -R monstack:monstack /var/lib/monstack /var/log/monstack /etc/monstack

USER monstack
EXPOSE 8443 9443

VOLUME ["/var/lib/monstack"]

ENTRYPOINT ["monstack", "server"]
CMD ["--config", "/etc/monstack/monstack.yaml"]
