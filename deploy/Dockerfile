FROM golang:1.26.5-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -trimpath -ldflags='-s -w' -o /monitor ./cmd/verone

FROM alpine:3.23
RUN apk add --no-cache ca-certificates && addgroup -S monitor && adduser -S -G monitor monitor
COPY --from=build /monitor /usr/local/bin/monitor
USER monitor
EXPOSE 8080
HEALTHCHECK --interval=30s --timeout=5s CMD wget -q --spider http://127.0.0.1:8080/healthz || exit 1
ENTRYPOINT ["/usr/local/bin/monitor"]
