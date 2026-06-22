# Multi-stage build. Build context is the repo root so the shared pkg module is available.
# Build:  docker build -f services/search/Dockerfile -t aizorix/search:dev .
FROM golang:1.22-alpine AS build
WORKDIR /src
RUN apk add --no-cache git
COPY go.work go.work
COPY services/pkg/ services/pkg/
COPY services/search/ services/search/
WORKDIR /src/services/search
RUN go mod download
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w" -o /out/search ./cmd/server

FROM gcr.io/distroless/static-debian12:nonroot
WORKDIR /
COPY --from=build /out/search /search
EXPOSE 8080 9090
USER nonroot:nonroot
ENTRYPOINT ["/search"]
