# syntax=docker/dockerfile:1

FROM node:22-bookworm-slim AS ui
WORKDIR /build/ui
# UI_BASE_PATH: set when this image is served under a path prefix behind a
# reverse proxy (e.g. "/ego/") instead of at domain root. Vite's --base
# fixes up the HTML/asset references; VITE_API_BASE_URL fixes up
# config.ts's API_BASE_URL, which every fetch/EventSource call is built
# from - both are required, or the app loads but every API call 404s
# against the domain root instead of the prefixed path.
ARG UI_BASE_PATH=/
COPY ui/package.json ui/package-lock.json ./
RUN npm ci
COPY ui/ ./
RUN VITE_API_BASE_URL="${UI_BASE_PATH%/}" npm run build -- --base="${UI_BASE_PATH}"

FROM golang:1.27-bookworm AS build
WORKDIR /build
COPY go.mod go.sum ./
RUN go mod download
COPY cmd ./cmd
COPY internal ./internal
# ui/embed.go's go:embed all:dist needs both the Go source and a built
# dist/ present before `go build` - the Go source comes straight from the
# repo, the assets from the "ui" stage above (ko builds this same package
# the same way, see ui/embed.go's doc comment).
COPY ui/embed.go ./ui/embed.go
COPY --from=ui /build/ui/dist ./ui/dist
# CGO_ENABLED=0: modernc.org/sqlite is pure Go, so the binary stays fully
# static - no libc dependency, safe on a distroless base.
ARG VERSION=dev
RUN CGO_ENABLED=0 go build -trimpath \
    -ldflags "-s -w -X github.com/triageagent-dev/ego/internal/api.Version=${VERSION}" \
    -o /ego ./cmd/ego

FROM gcr.io/distroless/static-debian12:nonroot
WORKDIR /app
COPY --from=build /ego /usr/local/bin/ego

EXPOSE 8001 4318

ENTRYPOINT ["ego"]
CMD ["serve", "--addr", ":8001", "--otlp-addr", ":4318", "--otlp-grpc-addr", ":4317", "--health-addr", ":9090"]
