# Multi-stage build. Build context is the repo root so the shared pkg module is available.
# Build:  docker build -f services/escrow/Dockerfile -t aizorix/escrow:dev .
FROM golang:1.25-alpine AS build
WORKDIR /src
RUN apk add --no-cache git
COPY services/pkg/ services/pkg/
COPY services/escrow/ services/escrow/
WORKDIR /src/services/escrow
RUN go mod download
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w" -o /out/escrow ./cmd/server

FROM gcr.io/distroless/static-debian12:nonroot
WORKDIR /
COPY --from=build /out/escrow /escrow
EXPOSE 8080 9090
USER nonroot:nonroot
ENTRYPOINT ["/escrow"]
