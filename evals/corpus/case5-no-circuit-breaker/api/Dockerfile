FROM golang:1.23-alpine AS build
WORKDIR /src
COPY main.go .
RUN go mod init checkout-api && CGO_ENABLED=0 go build -o /checkout-api .

FROM alpine:3.20
COPY --from=build /checkout-api /checkout-api
EXPOSE 8080
ENTRYPOINT ["/checkout-api"]
