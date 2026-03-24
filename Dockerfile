# syntax=docker/dockerfile:1.7

FROM node:22-bookworm-slim AS web-build
WORKDIR /src

COPY web/package.json web/package-lock.json ./web/
RUN npm --prefix web ci

COPY web/index.html web/tsconfig.json web/vite.config.ts ./web/
COPY web/src ./web/src
RUN npm --prefix web run build

FROM golang:1.24.5-bookworm AS go-build
WORKDIR /src

ENV CGO_ENABLED=0 GOOS=linux

COPY go.mod go.sum ./
RUN go mod download

COPY cmd ./cmd
COPY internal ./internal
COPY web/embed.go ./web/embed.go
COPY web/placeholder ./web/placeholder
COPY --from=web-build /src/web/dist ./web/dist

RUN go build -trimpath -ldflags='-s -w' -o /out/deckhand ./cmd/deckhand

FROM gcr.io/distroless/static-debian12:nonroot AS runtime
WORKDIR /

COPY --from=go-build /out/deckhand /deckhand

EXPOSE 8080
USER nonroot:nonroot
ENTRYPOINT ["/deckhand"]
