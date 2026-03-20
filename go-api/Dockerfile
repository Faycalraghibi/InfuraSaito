FROM golang:1.22-alpine AS builder

WORKDIR /app
COPY go.* ./
# No external dependencies yet, but good practice
RUN go mod download || true

COPY . .
RUN CGO_ENABLED=0 GOOS=linux go build -o forecasting-api .

FROM alpine:latest
WORKDIR /app
COPY --from=builder /app/forecasting-api .

EXPOSE 8080
CMD ["./forecasting-api"]
