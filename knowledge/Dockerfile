FROM golang:1.26-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN go vet ./... && CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /knowledge .

FROM alpine:3.23
RUN apk --no-cache add ca-certificates && addgroup -S blix && adduser -S -G blix blix \
    && mkdir -p /data/documents && chown -R blix:blix /data
ENV DOCUMENT_STORAGE_PATH=/data/documents
COPY --from=build /knowledge /usr/local/bin/knowledge
USER blix
EXPOSE 9988
ENTRYPOINT ["knowledge"]
