# syntax=docker/dockerfile:1.7@sha256:a57df69d0ea827fb7266491f2813635de6f17269be881f696fbfdf2d83dda33e

FROM node:24.18.0-bookworm-slim@sha256:d45d78e7929b46875bbd4e29bea672d5bc48186c6c3588306521c815e78352d6 AS ui-build
ARG SOURCE_DATE_EPOCH=0
ENV NPM_CONFIG_AUDIT=false \
    NPM_CONFIG_FUND=false
WORKDIR /build
COPY package.json package-lock.json ./
COPY hosted.html obs.html vite.hosted.config.ts ./
COPY src/hosted ./src/hosted
COPY src/gameplay-templates.ts src/display-themes.ts src/format.ts src/types.ts src/duration.ts src/gift-rule-operations.ts ./src/
RUN npm ci && npm run build:hosted

FROM golang:1.26.5-bookworm@sha256:0d327c83532d3cdeeeebab56ce85962bf09cb89545355b10207c7771b0c3713f AS server-build
ARG SOURCE_DATE_EPOCH=0
WORKDIR /build/goserver
COPY goserver/go.mod goserver/go.sum ./
RUN go mod download
COPY goserver/cmd/hosted/main.go goserver/cmd/hosted/metrics.go ./cmd/hosted/
COPY goserver/internal ./internal
RUN mkdir -p /out && CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath -ldflags='-s -w' -o /out/gift-panel-hosted ./cmd/hosted

FROM debian:bookworm-20260713-slim@sha256:63a496b5d3b99214b39f5ed70eb71a61e590a77979c79cbee4faf991f8c0783e
ARG SOURCE_DATE_EPOCH=0
COPY --from=server-build /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/ca-certificates.crt
COPY --from=server-build /out/gift-panel-hosted /usr/local/bin/gift-panel-hosted
COPY --from=ui-build /build/goserver/cmd/hosted/dist /srv/gift-panel-hosted/ui
RUN <<'EOF'
set -eu
cat > /usr/local/bin/hosted-entrypoint <<'SCRIPT'
#!/bin/sh
set -eu

read_secret() {
    secret_file="$1"
    [ -f "$secret_file" ] || exit 78
    IFS= read -r secret_value < "$secret_file" || [ -n "$secret_value" ]
    [ -n "$secret_value" ] || exit 78
    printf '%s' "$secret_value"
}

export HOSTED_MYSQL_DSN="$(read_secret "${HOSTED_MYSQL_DSN_FILE:?}")"
export HOSTED_ADMIN_CSRF_TOKEN="$(read_secret "${HOSTED_ADMIN_CSRF_TOKEN_FILE:?}")"
export HOSTED_SMTP_PASSWORD="$(read_secret "${HOSTED_SMTP_PASSWORD_FILE:?}")"
exec /usr/local/bin/gift-panel-hosted "$@"
SCRIPT
chmod 0555 /usr/local/bin/hosted-entrypoint /usr/local/bin/gift-panel-hosted
mkdir -p /srv/gift-panel-hosted/ui /tmp
chown -R 65532:65532 /srv/gift-panel-hosted /tmp
EOF
USER 65532:65532
EXPOSE 12500
ENTRYPOINT ["/usr/local/bin/hosted-entrypoint"]
