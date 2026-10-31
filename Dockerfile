# syntax=docker/dockerfile:1
FROM golang:1.27.3-bookworm@sha256:d9b6a1beb9474adc2a1ce842573639b44b2a0dd49b6669f51d3821baf9ec37c9 AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download all
COPY cmd ./cmd
COPY internal ./internal
COPY scripts/licenses ./scripts/licenses
RUN --mount=type=cache,target=/root/.cache/go-build go run ./scripts/licenses -out /out/licenses
RUN --mount=type=cache,target=/root/.cache/go-build CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/multiparse ./cmd/multiparse

FROM tiago2/cap:3.1.17@sha256:b47424a1b4ab3e6e7205ba1ed35fa25c8817c78bf0a82a0c7dac5d3393167a03 AS cap
USER root
ARG VCS_REF=unknown
LABEL org.opencontainers.image.source="https://github.com/IwasakiYouko/Multi-platform-Parse" \
      org.opencontainers.image.revision="${VCS_REF}" \
      org.opencontainers.image.title="Multi-platform-Parse Cap launcher" \
      org.opencontainers.image.base.name="docker.io/tiago2/cap:3.1.17"
RUN ["bun", "-e", "const fs = require('node:fs'); fs.mkdirSync('/secrets', {recursive:true, mode:0o700}); fs.chownSync('/secrets',1000,1000)"]
USER bun
COPY --from=build /out/multiparse /usr/local/bin/multiparse
COPY THIRD_PARTY_NOTICES.md internal/web/assets/NOTICE internal/web/assets/*.LICENSE /usr/local/share/doc/multiparse/
COPY --from=build /out/licenses/ /usr/local/share/doc/multiparse/
ENTRYPOINT ["/usr/local/bin/multiparse", "cap-run"]

FROM debian:bookworm-slim@sha256:99c0b25c46d0214d5a2091092e7908907dcaecd6a962cde0ac700acee222b6f3 AS app
ARG VCS_REF=unknown
LABEL org.opencontainers.image.source="https://github.com/IwasakiYouko/Multi-platform-Parse" \
      org.opencontainers.image.revision="${VCS_REF}" \
      org.opencontainers.image.title="Multi-platform-Parse"
RUN apt-get update && apt-get install -y --no-install-recommends ca-certificates ffmpeg tzdata && rm -rf /var/lib/apt/lists/* \
 && mkdir -p /data /media /secrets /certs /usr/local/share/doc/multiparse \
 && chown 1000:1000 /data /media /secrets /certs \
 && dpkg-query -W -f='${binary:Package}\t${Version}\t${source:Package}\t${source:Version}\n' > /usr/local/share/doc/multiparse/debian-packages.tsv
COPY --from=build /out/multiparse /usr/local/bin/multiparse
COPY THIRD_PARTY_NOTICES.md internal/web/assets/NOTICE internal/web/assets/*.LICENSE /usr/local/share/doc/multiparse/
COPY --from=build /out/licenses/ /usr/local/share/doc/multiparse/
USER 1000:1000
ENV DATA_DIR=/data CACHE_DIR=/media CAP_ADMIN_KEY_FILE=/secrets/cap-admin.key
WORKDIR /data
EXPOSE 10492
ENTRYPOINT ["/usr/local/bin/multiparse"]
CMD ["serve"]
