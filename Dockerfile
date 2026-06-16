FROM golang:1.26-alpine AS builder
COPY core/ /build/core/
COPY searcher-module/ /build/searcher-module/
COPY http-downloader/ /build/http-downloader/
COPY media-automation/ /build/media-automation/
WORKDIR /build/media-automation
RUN go mod download && CGO_ENABLED=0 go build -o /media-automation ./cmd/module
FROM alpine:3.21
RUN adduser -D -h /data app
USER app
WORKDIR /app
COPY --from=builder /media-automation .
ENTRYPOINT ["./media-automation"]
