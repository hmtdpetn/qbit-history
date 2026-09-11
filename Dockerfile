# syntax=docker/dockerfile:1.7
# Stage 1: build the web UI (no Node in the runtime image)
FROM node:22-alpine AS web
WORKDIR /src/web
COPY web/package.json web/package-lock.json ./
RUN npm ci --no-audit --no-fund
COPY web/ ./
RUN npm run build

# Stage 2: build the static Go binary (modernc.org/sqlite is pure Go: CGO_ENABLED=0)
FROM golang:1.26-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY cmd ./cmd
COPY internal ./internal
ARG VERSION=2.0.0
RUN CGO_ENABLED=0 GOFLAGS=-trimpath go build -ldflags="-s -w" -o /out/history ./cmd/history

# Stage 3: minimal non-root runtime; no shell, no package manager, no Docker socket
FROM gcr.io/distroless/static-debian12:nonroot
WORKDIR /app
COPY --from=build /out/history /app/history
COPY --from=web /src/webassets/dist /app/web
ENV HISTORY_LISTEN=:28637 HISTORY_DATA=/data HISTORY_MASTER_KEY=/run/secrets/master.key HISTORY_WEB=/app/web
VOLUME ["/data"]
EXPOSE 28637
USER 65532:65532
HEALTHCHECK --interval=30s --timeout=5s --start-period=20s --retries=3 CMD ["/app/history", "-healthcheck"]
ENTRYPOINT ["/app/history"]
