FROM golang:1.25-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY main.go .
RUN CGO_ENABLED=0 go build -o /checkout-api .

FROM alpine:3.20
COPY --from=build /checkout-api /checkout-api
EXPOSE 8080
ENTRYPOINT ["/checkout-api"]
