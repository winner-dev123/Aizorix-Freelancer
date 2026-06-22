# Multi-stage build. Build context is the repo root so the shared pkg module is available.
# Build:  docker build -f services/admin/Dockerfile -t aizorix/admin:dev .
FROM golang:1.22-alpine AS build
WORKDIR /src
RUN apk add --no-cache git
COPY go.work go.work
COPY services/pkg/ services/pkg/
COPY services/admin/ services/admin/
WORKDIR /src/services/admin
RUN go mod download
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w" -o /out/admin ./cmd/server

FROM gcr.io/distroless/static-debian12:nonroot
WORKDIR /
COPY --from=build /out/admin /admin
EXPOSE 8080 9090
USER nonroot:nonroot
ENTRYPOINT ["/admin"]
