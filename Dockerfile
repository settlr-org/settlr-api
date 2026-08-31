FROM golang:1.26-alpine AS builder
WORKDIR /app
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -o /settlr ./cmd/settlr

FROM alpine:3.20
WORKDIR /app
RUN apk add --no-cache ca-certificates
COPY --from=builder /settlr /app/settlr
COPY migrations ./migrations
COPY openapi.json ./openapi.json
RUN mkdir -p /app/uploads
EXPOSE 8080
CMD ["/app/settlr"]
