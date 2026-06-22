# Multi-stage build. Build context is the repo root so the shared pkg module is available.
# Build:  docker build -f services/review/Dockerfile -t aizorix/review:dev .
FROM golang:1.25-alpine AS build
WORKDIR /src
RUN apk add --no-cache git
COPY services/pkg/ services/pkg/
COPY services/review/ services/review/
WORKDIR /src/services/review
RUN go mod download
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w" -o /out/review ./cmd/server

FROM gcr.io/distroless/static-debian12:nonroot
WORKDIR /
COPY --from=build /out/review /review
EXPOSE 8080 9090
USER nonroot:nonroot
ENTRYPOINT ["/review"]
