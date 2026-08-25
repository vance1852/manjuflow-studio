# Build stage runs on the native builder architecture and cross compiles for the
# requested target, which keeps linux/amd64 and linux/arm64 images reproducible.
FROM --platform=$BUILDPLATFORM golang:1.22-alpine AS build

ENV CGO_ENABLED=0 \
    GOTOOLCHAIN=local

WORKDIR /src

COPY go.mod go.sum ./
RUN go mod download

COPY cmd ./cmd
COPY internal ./internal
COPY migrations ./migrations

ARG TARGETOS
ARG TARGETARCH
RUN GOOS=${TARGETOS:-linux} GOARCH=${TARGETARCH:-amd64} \
    go build -trimpath -ldflags "-s -w" -o /out/manjuflow-server ./cmd/server

FROM alpine:3.20

RUN adduser -D -u 10001 manju \
    && mkdir -p /data \
    && chown manju:manju /data

COPY --from=build /out/manjuflow-server /app/manjuflow-server

USER manju
WORKDIR /app

ENV MANJU_HTTP_ADDR=":8080" \
    MANJU_DB_PATH="/data/manjuflow.sqlite" \
    MANJU_LOG_LEVEL="info"

EXPOSE 8080
VOLUME ["/data"]

HEALTHCHECK --interval=15s --timeout=3s --start-period=5s --retries=3 \
    CMD wget -q -O- http://127.0.0.1:8080/readyz || exit 1

ENTRYPOINT ["/app/manjuflow-server"]
