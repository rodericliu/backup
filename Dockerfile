# Images of backup-server and backup-web (nginx serving the web UI).
#   REG=reg.ideawisegroup.com:1443/library
#   V=$(git describe --tags --always --dirty)
#   podman build --target server -t $REG/backup-server:$V --build-arg VERSION=$V .
#   podman build --target web    -t $REG/backup-web:$V .
# See docs/deploy-podman.md.

FROM docker.io/library/node:24-alpine AS ui
WORKDIR /src/web
COPY web/package.json web/package-lock.json ./
RUN npm ci
COPY web/ ./
RUN npm run build

FROM docker.io/library/golang:1.27-alpine AS build
ARG VERSION=dev
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY cmd/ cmd/
COPY internal/ internal/
COPY web/embed.go web/
COPY --from=ui /src/web/dist/ web/dist/
RUN CGO_ENABLED=0 go build -trimpath \
    -ldflags "-s -w -X github.com/rodericliu/backup/internal/version.Version=${VERSION}" \
    -o /out/backup-server ./cmd/backup-server

FROM docker.io/library/nginx:stable-alpine AS web
RUN rm -rf /usr/share/nginx/html/* /etc/nginx/conf.d/default.conf \
    && mkdir -p /etc/nginx/snippets /etc/nginx/certs /etc/nginx/backup-ca
COPY deploy/nginx/security-headers.conf deploy/nginx/backup-proxy.conf /etc/nginx/snippets/
COPY deploy/nginx/backup.conf /etc/nginx/conf.d/backup.conf
COPY --from=ui /src/web/dist/ /usr/share/nginx/html/
EXPOSE 80 443
HEALTHCHECK --interval=30s --timeout=5s --retries=3 \
    CMD wget -q -O /dev/null http://127.0.0.1:8081/nginx-health || exit 1

FROM docker.io/library/alpine:3.22 AS server
# tzdata: timezone in server.yaml; ca-certificates: assistant.base_url; curl: health check
RUN apk add --no-cache ca-certificates tzdata curl \
    && addgroup -S -g 10001 backup-server \
    && adduser -S -D -H -u 10001 -G backup-server backup-server \
    && install -d -m 750 -o backup-server -g backup-server /etc/backup-server
COPY --from=build /out/backup-server /usr/local/bin/backup-server
USER backup-server:backup-server
EXPOSE 8443 9443
HEALTHCHECK --interval=30s --timeout=5s --start-period=30s --retries=3 \
    CMD curl -fsk https://127.0.0.1:8443/healthz || exit 1
ENTRYPOINT ["/usr/local/bin/backup-server"]
CMD ["run", "-config", "/etc/backup-server/server.yaml"]
