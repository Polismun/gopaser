FROM golang:1.26-alpine AS builder

WORKDIR /app

COPY go.mod go.sum ./
RUN go mod download

COPY *.go ./

RUN CGO_ENABLED=0 go build -o parser .

COPY server/ ./server/
WORKDIR /app/server
RUN CGO_ENABLED=0 go build -o /app/httpserver .

WORKDIR /app

# --- Runtime ---
FROM alpine:3.21

WORKDIR /app
COPY --from=builder /app/parser .
COPY --from=builder /app/httpserver .

EXPOSE 8080
CMD ["./httpserver"]
