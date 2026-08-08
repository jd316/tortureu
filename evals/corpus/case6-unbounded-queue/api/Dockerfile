FROM golang:1.23-alpine AS build
WORKDIR /src
COPY main.go .
RUN go mod init ingest-api && CGO_ENABLED=0 go build -o /ingest-api .

FROM alpine:3.20
COPY --from=build /ingest-api /ingest-api
EXPOSE 8080
ENTRYPOINT ["/ingest-api"]
