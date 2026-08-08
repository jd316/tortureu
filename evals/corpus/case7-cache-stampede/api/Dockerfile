FROM golang:1.23-alpine AS build
WORKDIR /src
COPY go.mod go.sum main.go .
RUN CGO_ENABLED=0 go build -o /catalog-api .

FROM alpine:3.20
COPY --from=build /catalog-api /catalog-api
EXPOSE 8080
ENTRYPOINT ["/catalog-api"]
