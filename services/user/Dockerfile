# Multi-stage build. Build context is the repo root so the shared pkg module is available.
# Build:  docker build -f services/user/Dockerfile -t aizorix/user:dev .
FROM golang:1.25-alpine AS build
WORKDIR /src
RUN apk add --no-cache git
COPY services/pkg/ services/pkg/
COPY services/user/ services/user/
WORKDIR /src/services/user
RUN go mod download
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w" -o /out/user ./cmd/server

FROM gcr.io/distroless/static-debian12:nonroot
WORKDIR /
COPY --from=build /out/user /user
EXPOSE 8080 9090
USER nonroot:nonroot
ENTRYPOINT ["/user"]
