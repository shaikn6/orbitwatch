# syntax=docker/dockerfile:1
FROM golang:1.26 AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY cmd ./cmd
COPY internal ./internal
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/orbitwatch ./cmd/orbitwatch

FROM gcr.io/distroless/static-debian12:nonroot
WORKDIR /app
COPY --from=build /out/orbitwatch /app/orbitwatch
COPY --chown=nonroot:nonroot web /app/web
VOLUME /app/data
EXPOSE 8080
USER nonroot:nonroot
ENTRYPOINT ["/app/orbitwatch", "watch", "-serve", "0.0.0.0:8080", "-dir", "/app/data/raw", "-out", "/app/web/data"]
