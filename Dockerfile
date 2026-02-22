FROM golang:1.26 AS build
WORKDIR /src
COPY go.mod ./
COPY go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -o /out/opencortex ./cmd/opencortex

FROM debian:bookworm-slim
WORKDIR /app
RUN useradd -m -u 10001 opencortex
COPY --from=build /out/opencortex /usr/local/bin/opencortex
COPY config.example.yaml /app/config.yaml
RUN mkdir -p /app/data /app/backups && chown -R opencortex:opencortex /app
USER opencortex
EXPOSE 8080
ENTRYPOINT ["opencortex", "server", "--config", "/app/config.yaml"]

