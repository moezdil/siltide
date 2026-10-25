FROM golang:1.25-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
ARG VERSION=dev
RUN CGO_ENABLED=0 go build -trimpath -ldflags "-s -w -X main.version=${VERSION}" -o /siltide .

# A glibc base: NVML is a glibc .so, and the DaemonSet bind-mounts the host's
# libnvidia-ml.so.1 into it. Same config as Dockerfile.release.
FROM debian:12-slim
RUN mkdir -p /etc/siltide && echo 'insecure: true' > /etc/siltide/config.yaml
ENV SILTIDE_CONFIG=/etc/siltide/config.yaml
COPY --from=build /siltide /siltide
ENTRYPOINT ["/siltide"]
CMD ["--service", "--listen", ":9800"]
