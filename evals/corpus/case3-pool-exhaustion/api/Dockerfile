FROM golang:1.23-alpine AS build
WORKDIR /src
COPY go.mod go.sum main.go .
RUN CGO_ENABLED=0 go build -o /orders-api .

FROM alpine:3.20
COPY --from=build /orders-api /orders-api
EXPOSE 8080
ENTRYPOINT ["/orders-api"]
