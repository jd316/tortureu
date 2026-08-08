FROM golang:1.23-alpine AS build
WORKDIR /src
COPY main.go .
RUN go mod init control-api && CGO_ENABLED=0 go build -o /control-api .

FROM alpine:3.20
COPY --from=build /control-api /control-api
EXPOSE 8080
ENTRYPOINT ["/control-api"]
