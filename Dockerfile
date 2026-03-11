FROM golang:1.24-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -o /sonos ./cmd/sonos

FROM alpine:3.21
COPY --from=build /sonos /usr/local/bin/sonos
ENTRYPOINT ["sonos"]
