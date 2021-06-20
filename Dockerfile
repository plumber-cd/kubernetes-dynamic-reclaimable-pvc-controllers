FROM golang:1.16 AS build

ARG RELEASE_VERSION=dev
ENV IMPORT_PATH="github.com/plumber-cd/kubernetes-dynamic-reclaimable-pvc-controllers"
WORKDIR /go/delivery
COPY . .
RUN mkdir bin && go build \
    -ldflags "-X ${IMPORT_PATH}.Version=${RELEASE_VERSION}" \
    -o ./bin ./...

FROM debian:buster

COPY --from=build /go/delivery/bin /usr/bin
