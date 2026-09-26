# SPDX-FileCopyrightText: 2026 The ucs-exporter authors
#
# SPDX-License-Identifier: GPL-3.0-only

FROM --platform=$BUILDPLATFORM golang:1.27-alpine AS build
ARG TARGETOS TARGETARCH VERSION=dev REVISION=unknown
WORKDIR /src
COPY go.mod go.sum ./
RUN --mount=type=cache,target=/go/pkg/mod go mod download
COPY . .
RUN --mount=type=cache,target=/go/pkg/mod --mount=type=cache,target=/root/.cache/go-build \
    CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH go build -trimpath -buildvcs=false \
      -ldflags "-s -w -X github.com/prometheus/common/version.Version=${VERSION} -X github.com/prometheus/common/version.Revision=${REVISION}" \
      -o /out/ucs-exporter ./cmd/ucs-exporter

FROM gcr.io/distroless/static-debian12:nonroot
ARG VERSION=dev
LABEL org.opencontainers.image.title="ucs-exporter" \
      org.opencontainers.image.description="Prometheus exporter for Cisco UCS Manager" \
      org.opencontainers.image.version="${VERSION}" \
      org.opencontainers.image.licenses="GPL-3.0-only"
COPY --from=build /out/ucs-exporter /bin/ucs-exporter
USER 65532:65532
EXPOSE 3001
ENTRYPOINT ["/bin/ucs-exporter"]
CMD ["--config.file=/etc/ucs-exporter/config.yml"]
