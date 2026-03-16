FROM golang:1.26-alpine AS builder

WORKDIR /app

# Copy go.mod/go.sum and download deps
COPY go.mod go.sum ./
RUN go mod download

# Copy all Go source files
COPY *.go ./

# Build the parser binary
RUN go build -o parser .

# Build the HTTP server
COPY server/ ./server/
RUN cd server && go build -o /app/server .

# --- Runtime ---
FROM alpine:3.21

WORKDIR /app
COPY --from=builder /app/parser .
COPY --from=builder /app/server .

EXPOSE 8080
CMD ["./server"]
