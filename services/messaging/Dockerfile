# Multi-stage build. Build context is the repo root so the shared pkg module is available.
# Build:  docker build -f services/messaging/Dockerfile -t aizorix/messaging:dev .
FROM golang:1.25-alpine AS build
WORKDIR /src
RUN apk add --no-cache git
COPY services/pkg/ services/pkg/
COPY services/messaging/ services/messaging/
WORKDIR /src/services/messaging
RUN go mod download
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w" -o /out/messaging ./cmd/server

FROM gcr.io/distroless/static-debian12:nonroot
WORKDIR /
COPY --from=build /out/messaging /messaging
EXPOSE 8080 9090
USER nonroot:nonroot
ENTRYPOINT ["/messaging"]
