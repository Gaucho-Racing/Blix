FROM golang:1.26-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /discord .

FROM alpine:3.23
RUN apk --no-cache add ca-certificates && addgroup -S blix && adduser -S -G blix blix
COPY --from=build /discord /usr/local/bin/discord
USER blix
EXPOSE 9966
ENTRYPOINT ["discord"]
