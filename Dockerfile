FROM golang:1.26-bookworm AS build

WORKDIR /src

COPY go.mod go.sum ./
RUN go mod download

COPY cmd ./cmd
COPY internal ./internal

RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w" \
    -o /out/halyk-agent ./cmd/halyk-agent

FROM debian:bookworm-slim

RUN apt-get update && apt-get install -y --no-install-recommends \
        ca-certificates \
        poppler-utils \
        tesseract-ocr \
        tesseract-ocr-eng \
        tesseract-ocr-rus \
        tesseract-ocr-kaz \
    && rm -rf /var/lib/apt/lists/*

WORKDIR /app

COPY --from=build /out/halyk-agent /usr/local/bin/halyk-agent
COPY config ./config

RUN mkdir -p /app/data /app/.cache /app/artifacts /app/out /app/logs

ENTRYPOINT ["halyk-agent"]
CMD ["run"]
