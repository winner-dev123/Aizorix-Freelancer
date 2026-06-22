# Multi-stage build. Build context is the repo root so the shared pkg module is available.
# Build:  docker build -f services/wsgateway/Dockerfile -t aizorix/wsgateway:dev .
FROM golang:1.25-alpine AS build
WORKDIR /src
RUN apk add --no-cache git
# Copy the modules the wsgateway service depends on (workspace-style).
COPY services/pkg/ services/pkg/
COPY services/wsgateway/ services/wsgateway/
WORKDIR /src/services/wsgateway
RUN go mod download
# Static, stripped binary for a distroless/scratch runtime.
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w" -o /out/wsgateway ./cmd/server

FROM gcr.io/distroless/static-debian12:nonroot
WORKDIR /
COPY --from=build /out/wsgateway /wsgateway
EXPOSE 8080 9090
USER nonroot:nonroot
ENTRYPOINT ["/wsgateway"]
