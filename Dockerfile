# syntax=docker/dockerfile:1

# --- Stage 1: build the SvelteKit frontend ---
# Outputs static assets into /src/internal/httpapi/webdist (per the sveltekit() plugin in web/vite.config.ts).
# Built on the native build platform: the output is arch-independent.
FROM --platform=$BUILDPLATFORM node:22-alpine AS frontend
WORKDIR /src/web
COPY web/package.json web/package-lock.json ./
RUN npm ci
COPY web/ ./
RUN npm run build

# --- Stage 2: build the Go binary with the frontend embedded ---
# Cross-compiles natively for the target arch (no QEMU emulation needed).
FROM --platform=$BUILDPLATFORM golang:1.25-alpine AS backend
ARG TARGETOS TARGETARCH
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
# Overlay the freshly built frontend so go:embed picks it up.
COPY --from=frontend /src/internal/httpapi/webdist ./internal/httpapi/webdist
RUN CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH go build -ldflags="-s -w" -o /out/seeurchin ./cmd/seeurchin

# --- Stage 3: minimal runtime ---
FROM gcr.io/distroless/static-debian12 AS runtime
COPY --from=backend /out/seeurchin /seeurchin
ENV SEEURCHIN_ADDR=:5858 \
    SEEURCHIN_DB_PATH=/config/seeurchin.db
EXPOSE 5858
VOLUME ["/config"]
ENTRYPOINT ["/seeurchin"]
