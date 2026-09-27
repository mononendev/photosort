# API + job runner: the Go server. Pixels go to the analyzer sidecar (docker/analyzer.Dockerfile) over loopback.
FROM golang:1.27-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN --mount=type=cache,target=/go/pkg/mod go mod download
COPY cmd ./cmd
COPY internal ./internal
ARG VERSION=dev
RUN --mount=type=cache,target=/go/pkg/mod --mount=type=cache,target=/root/.cache/go-build \
    CGO_ENABLED=0 go build -trimpath -ldflags "-s -w -X main.version=${VERSION}" -o /out/photosort ./cmd/photosort

# exiftool reads Canon AF points from files the built-in reader can't (CR3, odd maker notes).
FROM alpine:3.22 AS production
RUN apk add --no-cache ca-certificates tzdata exiftool \
 && addgroup -g 568 photosort && adduser -u 568 -G photosort -H -D -s /sbin/nologin photosort
COPY --from=build /out/photosort /usr/local/bin/photosort
ARG VERSION=dev
ARG GIT_SHA=unknown
ARG BUILD_TIME=
ENV PHOTOSORT_VERSION=${VERSION} PHOTOSORT_GIT_SHA=${GIT_SHA} PHOTOSORT_BUILD_TIME=${BUILD_TIME} \
    PHOTOSORT_WORKDIR=/data PHOTOSORT_PHOTOS=/photos PHOTOSORT_MODELS=/models PORT=8080 \
    PHOTOSORT_ANALYZER=http://127.0.0.1:8090
USER 568:568
EXPOSE 8080
ENTRYPOINT ["photosort"]
CMD ["web"]
