# syntax=docker/dockerfile:1
# One Dockerfile builds every deployable process: the SERVICE build argument names a
# directory under cmd/ (ingestion, indexing, search, control).

FROM golang:1.25-alpine AS builder
ARG SERVICE
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/boogle ./cmd/${SERVICE}

FROM alpine:3.21
RUN apk add --no-cache ca-certificates \
    && adduser -D -H -u 10001 boogle \
    && mkdir -p /data/datalake /data/control \
    && chown -R boogle:boogle /data
COPY --from=builder /out/boogle /usr/local/bin/boogle
USER boogle
ENTRYPOINT ["/usr/local/bin/boogle"]
