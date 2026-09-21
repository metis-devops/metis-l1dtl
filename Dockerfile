# syntax=docker/dockerfile:1
FROM golang:1.27.1-alpine AS build
WORKDIR /app
RUN apk add --no-cache ca-certificates git build-base
COPY . ./
RUN --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    go build -trimpath -ldflags="-s -w" -o ./bin/ ./cmd/...

FROM alpine:latest
RUN apk add --no-cache ca-certificates
COPY --from=build /app/bin/metis-l1dtl /usr/local/bin/metis-l1dtl
EXPOSE 7878
VOLUME ["/data"]
HEALTHCHECK --interval=30s --timeout=5s --start-period=10s --retries=3 \
    CMD ["/usr/local/bin/metis-l1dtl", "healthcheck"]
ENTRYPOINT ["/usr/local/bin/metis-l1dtl"]
