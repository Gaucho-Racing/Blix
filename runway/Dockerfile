FROM golang:1.26-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN go vet ./... && CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /runway .

FROM alpine:3.23
RUN apk --no-cache add ca-certificates && addgroup -S blix && adduser -S -G blix blix
COPY --from=build /runway /usr/local/bin/runway
USER blix
EXPOSE 9999
ENTRYPOINT ["runway"]
