# syntax=docker/dockerfile:1
FROM golang:1.27.3-alpine@sha256:fa3d596898d9f126af7c01d14ee7597bab3f39cccf392ef36b69fe77cd681f18 AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download all
COPY cmd ./cmd
COPY internal ./internal
COPY scripts/licenses ./scripts/licenses
RUN --mount=type=cache,target=/root/.cache/go-build go run ./scripts/licenses -out /out/licenses
RUN --mount=type=cache,target=/root/.cache/go-build CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/multiparse ./cmd/multiparse

# Only build tooling has pip; the isolated runtime contains locked yt-dlp default dependencies.
FROM alpine:3.24@sha256:f59abe656b692d901e184be3445e084786602108599d2a51d229c0f5a48c71bf AS ytdlp-runtime
RUN apk add --no-cache ca-certificates python3 py3-pip
COPY scripts/install_ytdlp_runtime.py scripts/ytdlp-runtime.lock.json /runtime/
RUN /usr/bin/python3 /runtime/install_ytdlp_runtime.py --skip-deno

FROM alpine:3.24@sha256:f59abe656b692d901e184be3445e084786602108599d2a51d229c0f5a48c71bf AS app
ARG VCS_REF=unknown
LABEL org.opencontainers.image.source="https://github.com/IwasakiYouko/Multi-platform-Parse" \
      org.opencontainers.image.revision="${VCS_REF}" \
      org.opencontainers.image.title="Multi-platform-Parse"
RUN apk add --no-cache ca-certificates ffmpeg tzdata python3 deno \
 && (getent group 1000 >/dev/null || addgroup -g 1000 -S multiparse) \
 && (getent passwd 1000 >/dev/null || adduser -u 1000 -S -D -h /tmp -G "$(getent group 1000 | cut -d: -f1)" multiparse) \
 && mkdir -p /usr/local/bin /data /media /certs /usr/local/share/doc/multiparse \
 && ln -sf /usr/bin/deno /usr/local/bin/deno \
 && chown 1000:1000 /data /media /certs \
 && apk info -v | sort > /usr/local/share/doc/multiparse/alpine-packages.txt
COPY --from=build /out/multiparse /usr/local/bin/multiparse
COPY THIRD_PARTY_NOTICES.md /usr/local/share/doc/multiparse/
COPY --from=build /out/licenses/ /usr/local/share/doc/multiparse/
COPY --from=ytdlp-runtime /opt/yt-dlp/ /opt/yt-dlp/
COPY --from=ytdlp-runtime /usr/local/share/doc/multiparse/ytdlp-runtime/ /usr/local/share/doc/multiparse/ytdlp-runtime/
USER 1000:1000
ENV DATA_DIR=/data CACHE_DIR=/media \
    YTDLP_PYTHON=/opt/yt-dlp/bin/python YTDLP_JS_RUNTIME=/usr/bin/deno \
    DENO_NO_UPDATE_CHECK=1 DENO_NO_PROMPT=1
WORKDIR /data
EXPOSE 10492
ENTRYPOINT ["/usr/local/bin/multiparse"]
CMD ["serve"]
