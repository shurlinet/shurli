# Multi-stage Dockerfile for Shurli integration testing.
# Builds a coverage-instrumented binary so Docker tests contribute to
# overall coverage via GOCOVERDIR + `go tool covdata merge`.

FROM golang:1.25-alpine AS builder
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -cover -o /shurli ./cmd/shurli

# --- Relay server image ---
FROM alpine:3.21 AS relay
RUN apk add --no-cache ca-certificates
COPY --from=builder /shurli /usr/local/bin/shurli
RUN mkdir -p /covdata
WORKDIR /data
ENTRYPOINT ["shurli"]

# --- Node (shurli client/daemon) image ---
FROM alpine:3.21 AS node
RUN apk add --no-cache ca-certificates
COPY --from=builder /shurli /usr/local/bin/shurli
RUN mkdir -p /root/.config/shurli /covdata
WORKDIR /root
ENTRYPOINT ["shurli"]
