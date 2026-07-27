# syntax=docker/dockerfile:1.7

# ─── Build ────────────────────────────────────────────────────────────────────
# Must be >= the `go` directive in go.mod, which the x/net and x/text security
# upgrades raised to 1.25. Kept in step with the local dev toolchain so a build
# that passes on a laptop passes on the server.
FROM golang:1.26-alpine AS build

WORKDIR /src

# Manifests first so dependencies only re-download when go.mod/go.sum change.
COPY go.mod go.sum ./
RUN --mount=type=cache,target=/go/pkg/mod \
    go mod download

COPY . .

# CGO_ENABLED=0 works because this project uses modernc.org/sqlite, a pure-Go
# SQLite. With mattn/go-sqlite3 this would need cgo and a matching libc in the
# runtime image, and the resulting binary wouldn't start.
# The cache mounts keep module/compiler caches out of the image while making
# rebuilds on a small VPS much faster.
RUN --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    CGO_ENABLED=0 GOOS=linux go build \
    -trimpath -ldflags="-s -w" \
    -o /out/prebuilt .

# ─── Runtime ──────────────────────────────────────────────────────────────────
FROM alpine:3.20

LABEL org.opencontainers.image.source="https://github.com/hunterMotko/prebuilt"

# ca-certificates: outbound SMTP over TLS fails without a trust store, which
#   would make the contact form silently stop emailing.
# tzdata: SQLite timestamps are scanned into time.Time.
# sqlite: the only way to edit the siding_colors/roof_colors tables today.
RUN apk add --no-cache ca-certificates tzdata sqlite \
    && adduser -D -u 10001 -h /app app

WORKDIR /app

COPY --from=build /out/prebuilt /app/prebuilt

# Templates and static assets are resolved by RELATIVE path at runtime
# (template.ParseGlob("templates/*.html"), e.Static("/public", "public")), so
# they must live under this WORKDIR or the app panics on startup.
COPY templates/ /app/templates/
COPY public/ /app/public/

# Created and chowned here so the app can write even with no volume attached.
# When a named volume is mounted at either path, Docker seeds the volume with
# this ownership — which is why the compose setup needs no chown step.
RUN mkdir -p /data /app/public/images/inventory \
    && chown -R app:app /data /app/public/images/inventory

USER app

ENV PORT=8080 \
    DB_PATH=/data/prebuilt.db

EXPOSE 8080

HEALTHCHECK --interval=30s --timeout=3s --start-period=10s --retries=3 \
    CMD wget -q -O /dev/null http://127.0.0.1:8080/ || exit 1

ENTRYPOINT ["/app/prebuilt"]
