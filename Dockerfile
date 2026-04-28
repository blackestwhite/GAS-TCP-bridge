FROM golang:1.22-alpine AS build

WORKDIR /src
COPY . .
RUN go build -o /out/gas-tcp-server ./cmd/server

FROM alpine:3.20

RUN adduser -D -H bridge
USER bridge
COPY --from=build /out/gas-tcp-server /usr/local/bin/gas-tcp-server
EXPOSE 8080
ENTRYPOINT ["gas-tcp-server"]
