# syntax=docker/dockerfile:1.7

# ─── Build stage ──────────────────────────────────────────────────────────────
FROM golang:1.22-alpine3.20 AS build

WORKDIR /src

# Manifests in their own layer so dependencies are only re-fetched when
# go.mod/go.sum actually change, not on every source edit.
COPY go.mod go.sum ./
RUN --mount=type=cache,target=/go/pkg/mod \
    go mod download

COPY . .

# CGO_ENABLED=0 works here *specifically* because this project uses
# modernc.org/sqlite, a pure-Go SQLite implementation. The more common
# mattn/go-sqlite3 would need cgo plus a matching libc in the runtime image and
# this line would produce a binary that won't start.
#
# -trimpath strips local filesystem paths out of the binary.
# -s -w drop the symbol table and DWARF debug info — nothing needs them at
# runtime, and it meaningfully shrinks the image.
#
# The cache mounts keep the module and compiler caches out of the image layers
# while still making rebuilds fast. They need BuildKit, which is the default in
# Docker 23+ and Compose v2.
RUN --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    CGO_ENABLED=0 GOOS=linux go build \
    -trimpath -ldflags="-s -w" \
    -o /out/prebuilt .

# ─── Runtime stage ────────────────────────────────────────────────────────────
# Alpine rather than distroless/scratch, deliberately. Each package earns its
# place:
#   ca-certificates — outbound SMTP over TLS fails with no trust store, which
#                     would make the contact form silently stop emailing.
#   tzdata          — SQLite timestamps get scanned into time.Time.
#   sqlite          — CLAUDE.md documents editing the siding_colors/roof_colors
#                     reference tables through the sqlite3 CLI, and there is
#                     still no admin UI for them. Without this, that documented
#                     workflow needs a second image just to reach the database.
# Alpine also keeps a shell, so `docker compose exec app sh` works when
# something is wrong on the server. Distroless would be a smaller attack
# surface but gives up all of the above.
FROM alpine:3.20

LABEL org.opencontainers.image.title="Prebuilt Sheds LLC" \
      org.opencontainers.image.description="Go + Echo marketing site with SQLite-backed shed inventory" \
      org.opencontainers.image.source="https://github.com/hunterMotko/prebuilt"

RUN apk add --no-cache ca-certificates tzdata sqlite \
    && adduser -D -u 10001 -h /app app

WORKDIR /app

COPY --from=build /out/prebuilt /app/prebuilt

# Templates and static assets are resolved by *relative* path at runtime
# (template.ParseGlob("templates/*.html") and e.Static("/public", "public")),
# so they have to sit under this WORKDIR or the app panics on startup.
COPY templates/ /app/templates/
COPY public/ /app/public/

# Exactly two writable paths, both intended as mount points:
#   /data                        → the SQLite database (see DB_PATH below)
#   /app/public/images/inventory → uploaded shed photos
#
# The upload directory is deliberately *inside* public/: templates reference
# photos as /public/images/inventory/<id>/<file>, so that URL path is fixed.
# Mount a volume over all of public/ and it hides the CSS, JS, and logo baked
# into the image — mount only this subdirectory.
RUN mkdir -p /data /app/public/images/inventory \
    && chown -R app:app /data /app/public/images/inventory

USER app

ENV PORT=8080 \
    DB_PATH=/data/prebuilt.db

EXPOSE 8080

# Distinguishes "container is running" from "app is actually serving", so a
# hung process gets reported as unhealthy instead of looking fine.
HEALTHCHECK --interval=30s --timeout=3s --start-period=10s --retries=3 \
    CMD wget -q -O /dev/null http://127.0.0.1:8080/ || exit 1

ENTRYPOINT ["/app/prebuilt"]
