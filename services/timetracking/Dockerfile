FROM golang:1.25-alpine AS build
WORKDIR /src
RUN apk add --no-cache git
COPY services/pkg/ services/pkg/
COPY services/timetracking/ services/timetracking/
WORKDIR /src/services/timetracking
RUN go mod download
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w" -o /out/timetracking ./cmd/server

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /out/timetracking /timetracking
EXPOSE 8080 9090
USER nonroot:nonroot
ENTRYPOINT ["/timetracking"]
