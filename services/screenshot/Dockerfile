FROM golang:1.22-alpine AS build
WORKDIR /src
RUN apk add --no-cache git
COPY go.work go.work
COPY services/pkg/ services/pkg/
COPY services/screenshot/ services/screenshot/
WORKDIR /src/services/screenshot
RUN go mod download
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w" -o /out/screenshot ./cmd/server

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /out/screenshot /screenshot
EXPOSE 8080 9090
USER nonroot:nonroot
ENTRYPOINT ["/screenshot"]
