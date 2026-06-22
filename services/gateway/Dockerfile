# Multi-stage build. Build context is the repo root so the shared pkg module is available.
# Build:  docker build -f services/gateway/Dockerfile -t aizorix/gateway:dev .
FROM golang:1.22-alpine AS build
WORKDIR /src
RUN apk add --no-cache git
# Copy the modules the gateway depends on. The gateway is a standalone module
# (resolved via its own go.mod `replace ... => ../pkg`), so it builds with the
# workspace disabled — it is not part of go.work, which the orchestrator owns.
COPY services/pkg/ services/pkg/
COPY services/gateway/ services/gateway/
WORKDIR /src/services/gateway
ENV GOWORK=off
RUN go mod download
# Static, stripped binary for a distroless/scratch runtime.
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w" -o /out/gateway ./cmd/server

FROM gcr.io/distroless/static-debian12:nonroot
WORKDIR /
COPY --from=build /out/gateway /gateway
EXPOSE 8080 9090
USER nonroot:nonroot
ENTRYPOINT ["/gateway"]
