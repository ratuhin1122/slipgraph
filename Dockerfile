FROM golang:1.26-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY cmd/ ./cmd/
COPY internal/ ./internal/
COPY db/migrations/ ./db/migrations/
RUN --mount=type=cache,target=/root/.cache/go-build CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/api ./cmd/api &&     CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/worker ./cmd/worker &&     CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/migrate ./cmd/migrate && CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/slipgraph ./cmd/slipgraph

FROM alpine:3.23 AS runtime
RUN apk add --no-cache ca-certificates && addgroup -g 10001 app && adduser -D -u 10001 -G app app
WORKDIR /app
COPY --from=build /out/ /app/
COPY db/migrations /app/db/migrations
USER 10001:10001
EXPOSE 8080 8081
CMD ["/app/api"]
