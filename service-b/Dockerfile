FROM golang:1.24.1-alpine AS builder

WORKDIR /app

COPY go.mod go.sum ./
RUN go mod download

COPY . .
RUN go build -o service-b .

FROM alpine:latest

WORKDIR /app

COPY --from=builder /app/service-b .

EXPOSE 8081

CMD ["./service-b"]
