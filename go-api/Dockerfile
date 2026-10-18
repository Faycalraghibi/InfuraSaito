FROM golang:1.22-alpine AS builder

WORKDIR /app
COPY go.* ./
# Download dependencies in their own layer so code changes don't refetch them.
ENV GOTOOLCHAIN=local
RUN go mod download

COPY . .
RUN CGO_ENABLED=0 GOOS=linux go build -o forecasting-api .

FROM alpine:3.20
WORKDIR /app
COPY --from=builder /app/forecasting-api .

EXPOSE 8080
CMD ["./forecasting-api"]
