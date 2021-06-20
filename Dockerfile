FROM golang:1.16 AS build

WORKDIR /go/delivery
COPY . .
RUN mkdir bin && go build -o ./bin ./...

FROM debian:buster

COPY --from=build /go/delivery/bin /usr/bin
