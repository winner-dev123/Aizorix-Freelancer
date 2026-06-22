# Multi-stage build. Build context is the repo root so the shared pkg module is available.
# Build:  docker build -f services/project/Dockerfile -t aizorix/project:dev .
FROM golang:1.22-alpine AS build
WORKDIR /src
RUN apk add --no-cache git
COPY go.work go.work
COPY services/pkg/ services/pkg/
COPY services/project/ services/project/
WORKDIR /src/services/project
RUN go mod download
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w" -o /out/project ./cmd/server

FROM gcr.io/distroless/static-debian12:nonroot
WORKDIR /
COPY --from=build /out/project /project
EXPOSE 8080 9090
USER nonroot:nonroot
ENTRYPOINT ["/project"]
