FROM golang:1.26-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -o /gateway ./cmd/gateway

FROM alpine:3.20
RUN apk add --no-cache ca-certificates
COPY --from=build /gateway /usr/local/bin/gateway
EXPOSE 8080 9090
ENTRYPOINT ["gateway"]
