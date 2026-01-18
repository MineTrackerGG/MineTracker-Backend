FROM golang:1.25.6-alpine AS builder

WORKDIR /app

RUN apk add --no-cache git

COPY go.mod go.sum ./
RUN go mod download
COPY . .

RUN --mount=type=cache,target=/root/.cache/go-build \
    --mount=type=cache,target=/go/pkg/mod \
    CGO_ENABLED=0 GOOS=linux go build -o minetracker .

FROM alpine:latest

WORKDIR /app
RUN apk --no-cache add ca-certificates
COPY --from=builder /app/minetracker .

RUN mkdir -p /app/data

CMD ["./minetracker"]
