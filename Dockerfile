FROM golang:1.25-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -buildvcs=false -trimpath -ldflags="-s -w" -o /bot ./cmd/bot

FROM alpine:3.23
RUN apk add --no-cache ca-certificates && addgroup -g 10001 bot && adduser -D -u 10001 -G bot bot && mkdir /data && chown bot:bot /data
COPY --from=build /bot /usr/local/bin/bot
USER bot
EXPOSE 8080
ENTRYPOINT ["bot"]
