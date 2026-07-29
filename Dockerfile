FROM golang:1.24-alpine AS build
WORKDIR /src
COPY go.mod go.sum* ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /webhook-inspector ./cmd/server

FROM alpine:3.21
RUN addgroup -S app && adduser -S app -G app
WORKDIR /app
COPY --from=build /webhook-inspector /usr/local/bin/webhook-inspector
RUN mkdir /app/data && chown app:app /app/data
USER app
EXPOSE 8080
ENV ADDR=:8080 DATABASE_PATH=/app/data/webhooks.db
ENTRYPOINT ["webhook-inspector"]
