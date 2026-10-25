FROM golang:1.27-alpine@sha256:b8c9b82affbbb82a020bbcf998dad2ba9516944ac7e883524642a481a9505e9c AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
ARG VERSION=dev
RUN CGO_ENABLED=0 go build -trimpath -ldflags "-s -w -X main.version=${VERSION}" -o /siltide .

# A glibc base: NVML is a glibc .so, and the DaemonSet bind-mounts the host's
# libnvidia-ml.so.1 into it. Same config as Dockerfile.release.
FROM debian:13-slim@sha256:62bd423aeee912639ca8ee681c2b762cb3e25137e0a92b20be455898a9f034ad
RUN mkdir -p /etc/siltide && echo 'insecure: true' > /etc/siltide/config.yaml
ENV SILTIDE_CONFIG=/etc/siltide/config.yaml
COPY --from=build /siltide /siltide
ENTRYPOINT ["/siltide"]
CMD ["--service", "--listen", ":9800"]
