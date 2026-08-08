FROM golang:1.23-alpine AS build
WORKDIR /src
COPY main.go .
RUN go mod init dep && CGO_ENABLED=0 go build -o /dep .

FROM alpine:3.20
COPY --from=build /dep /dep
EXPOSE 9090
ENTRYPOINT ["/dep"]
