FROM golang:1.25-alpine AS builder

WORKDIR /src

COPY go.mod go.sum ./
COPY vendor ./vendor

COPY . .
RUN CGO_ENABLED=0 GOOS=linux go build -mod=vendor -trimpath -ldflags="-s -w" -o /out/silent-room .

FROM alpine:3.22

RUN addgroup -S app \
    && adduser -S -G app app

WORKDIR /app
COPY --from=builder /out/silent-room /app/silent-room

USER app
EXPOSE 8080

CMD ["/app/silent-room"]
