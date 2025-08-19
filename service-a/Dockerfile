FROM golang:1.24.1-alpine AS builder

WORKDIR /app

COPY go.mod go.sum ./
RUN go mod download

COPY . .
RUN go build -o service-a .

FROM alpine:latest

WORKDIR /app

COPY --from=builder /app/service-a .

EXPOSE 8080

CMD ["./service-a"]
