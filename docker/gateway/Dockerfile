FROM golang:1.25-bookworm AS build

WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY main.go ./
COPY app ./app
COPY workflows ./workflows
RUN CGO_ENABLED=0 go build -trimpath -o /go-gateway .

FROM debian:bookworm-slim

RUN apt-get update \
    && apt-get install -y --no-install-recommends ca-certificates curl ffmpeg \
    && rm -rf /var/lib/apt/lists/*

WORKDIR /app
COPY --from=build /go-gateway /usr/local/bin/go-gateway
COPY --from=build /src/app ./app
COPY --from=build /src/workflows ./workflows

ENTRYPOINT ["/usr/local/bin/go-gateway"]
