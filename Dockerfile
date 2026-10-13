FROM golang:1.27.2-bookworm@sha256:5cf287a799e6b94384bad13d16b14904c531f51ba65792237e122ce42b392f61 AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY cmd/ ./cmd/
COPY internal/ ./internal/
RUN CGO_ENABLED=0 go build -trimpath -ldflags='-s -w' -o /out/adapter ./cmd/adapter

FROM alpine:3.22.2@sha256:4b7ce07002c69e8f3d704a9c5d6fd3053be500b7f1c69fc0d80990c2ad8dd412
RUN addgroup -g 10001 adapter && adduser -D -u 10001 -G adapter adapter && mkdir /data && chown 10001:10001 /data && chmod 700 /data
COPY --from=build /out/adapter /usr/local/bin/adapter
COPY LICENSE THIRD_PARTY.md /licenses/
COPY licenses/ /licenses/dependencies/
USER 10001:10001
EXPOSE 8080 9090
HEALTHCHECK --interval=10s --timeout=3s --retries=3 CMD ["/usr/local/bin/adapter", "-healthcheck", "http://127.0.0.1:9090/readyz"]
ENTRYPOINT ["/usr/local/bin/adapter"]
CMD ["-config", "/etc/adapter/config.json"]
