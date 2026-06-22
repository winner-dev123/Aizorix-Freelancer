# Multi-stage build. Build context is the repo root so the shared pkg module is available.
# Build:  docker build -f services/analytics/Dockerfile -t aizorix/analytics:dev .
FROM golang:1.22-alpine AS build
WORKDIR /src
RUN apk add --no-cache git
COPY go.work go.work
COPY services/pkg/ services/pkg/
COPY services/analytics/ services/analytics/
WORKDIR /src/services/analytics
RUN go mod download
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w" -o /out/analytics ./cmd/server

FROM gcr.io/distroless/static-debian12:nonroot
WORKDIR /
COPY --from=build /out/analytics /analytics
EXPOSE 8080 9090
USER nonroot:nonroot
ENTRYPOINT ["/analytics"]
