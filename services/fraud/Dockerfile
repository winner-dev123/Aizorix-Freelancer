# Multi-stage build. Build context is the repo root so the shared pkg module is available.
# Build:  docker build -f services/fraud/Dockerfile -t aizorix/fraud:dev .
FROM golang:1.25-alpine AS build
WORKDIR /src
RUN apk add --no-cache git
COPY services/pkg/ services/pkg/
COPY services/fraud/ services/fraud/
WORKDIR /src/services/fraud
RUN go mod download
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w" -o /out/fraud ./cmd/server

FROM gcr.io/distroless/static-debian12:nonroot
WORKDIR /
COPY --from=build /out/fraud /fraud
EXPOSE 8080 9090
USER nonroot:nonroot
ENTRYPOINT ["/fraud"]
