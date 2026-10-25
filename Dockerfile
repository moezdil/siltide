FROM golang:1.25-alpine@sha256:1ae0735f00daffa3aaf1363a5184c0d2dc55c78e3db4ec70241cdac97bf84b59 AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
ARG VERSION=dev
RUN CGO_ENABLED=0 go build -trimpath -ldflags "-s -w -X main.version=${VERSION}" -o /siltide .

# A glibc base: NVML is a glibc .so, and the DaemonSet bind-mounts the host's
# libnvidia-ml.so.1 into it. Same config as Dockerfile.release.
FROM debian:12-slim@sha256:7ff6ba31527b9e336162fa061746c366c4b2032f4d2cff2078bea43f78efe3f8
RUN mkdir -p /etc/siltide && echo 'insecure: true' > /etc/siltide/config.yaml
ENV SILTIDE_CONFIG=/etc/siltide/config.yaml
COPY --from=build /siltide /siltide
ENTRYPOINT ["/siltide"]
CMD ["--service", "--listen", ":9800"]
