# Multi-stage build. Build context is the repo root so the shared pkg module is available.
# Build:  docker build -f services/auth/Dockerfile -t aizorix/auth:dev .
FROM golang:1.22-alpine AS build
WORKDIR /src
RUN apk add --no-cache git
# Copy the modules the auth service depends on (workspace-style).
COPY go.work go.work
COPY services/pkg/ services/pkg/
COPY services/auth/ services/auth/
WORKDIR /src/services/auth
RUN go mod download
# Static, stripped binary for a distroless/scratch runtime.
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w" -o /out/auth ./cmd/server

FROM gcr.io/distroless/static-debian12:nonroot
WORKDIR /
COPY --from=build /out/auth /auth
EXPOSE 8080 9090
USER nonroot:nonroot
ENTRYPOINT ["/auth"]
