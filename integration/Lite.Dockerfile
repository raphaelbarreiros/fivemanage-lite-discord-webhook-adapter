FROM node:22.14.0-bookworm-slim@sha256:1c18d9ab3af4585870b92e4dbc5cac5a0dc77dd13df1a5905cea89fc720eb05b AS web
WORKDIR /web
RUN npm install --global pnpm@10.13.1
COPY .cache/lite/web/package.json .cache/lite/web/pnpm-lock.yaml ./
RUN pnpm install --frozen-lockfile
COPY .cache/lite/web/ ./
RUN pnpm build

FROM golang:1.27.2-bookworm@sha256:5cf287a799e6b94384bad13d16b14904c531f51ba65792237e122ce42b392f61 AS build
WORKDIR /src
COPY .cache/lite/go.mod .cache/lite/go.sum ./
RUN go mod download
COPY .cache/lite/ ./
COPY --from=web /web/dist ./internal/http/dist
ARG LITE_PATCHED=1
COPY patches/lite/0001-confirm-ingestion.patch /fix.patch
RUN if [ "$LITE_PATCHED" = 1 ]; then git apply /fix.patch; fi
RUN go run github.com/swaggo/swag/cmd/swag@v1.16.6 init -g cmd/lite/lite.go --parseDependency --parseInternal
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w -X main.Version=74ba491-patch${LITE_PATCHED}" -o /out/lite ./cmd/lite

FROM alpine:3.22.2@sha256:4b7ce07002c69e8f3d704a9c5d6fd3053be500b7f1c69fc0d80990c2ad8dd412
RUN addgroup -g 10001 lite && adduser -D -u 10001 -G lite lite
COPY --from=build /out/lite /usr/local/bin/lite
COPY .cache/lite/LICENSE.md /licenses/Lite-LICENSE.md
# project.GetRoot uses runtime.Caller; -trimpath produces this module-relative root.
COPY .cache/lite/internal/clickhouse/migrations/ /github.com/fivemanage/lite/internal/clickhouse/migrations/
COPY integration/lite-entrypoint.sh /usr/local/bin/lite-entrypoint
USER 10001:10001
EXPOSE 8080
ENTRYPOINT ["/bin/sh", "/usr/local/bin/lite-entrypoint"]
