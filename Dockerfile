FROM golang:1.22-alpine AS build

WORKDIR /app

COPY go.mod go.sum ./
RUN go mod download

COPY . .

RUN CGO_ENABLED=0 GOOS=linux go build -o /bot ./cmd/bot

FROM alpine:3.20

WORKDIR /app

COPY --from=build /bot /usr/local/bin/bot

CMD ["bot"]
