# AIGis open-source gateway image (AGPLv3 build, no ee/).
FROM golang:1.25-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
ARG VERSION=docker
RUN CGO_ENABLED=0 go build -ldflags "-s -w -X main.version=${VERSION}" -o /out/aigis ./cmd/aigis

FROM alpine:3.20
# ca-certificates: TLS to upstream LLM APIs; wget ships with busybox (healthcheck).
RUN apk add --no-cache ca-certificates && adduser -D -H -u 10001 aigis
WORKDIR /app
COPY --from=build /out/aigis /app/aigis
COPY configs/config.yaml /app/configs/config.yaml
# ./logs holds aigis.log + audit.jsonl (metadata-only masking audit trail).
RUN mkdir -p /app/logs /app/configs && \
    chmod -R a+rX /app && \
    chown -R aigis /app/logs /app/configs
USER aigis
EXPOSE 8080
HEALTHCHECK --interval=30s --timeout=3s --start-period=5s \
  CMD wget -qO- http://127.0.0.1:8080/health || exit 1
ENTRYPOINT ["/app/aigis"]
CMD ["serve"]
