FROM golang:1.26-alpine AS build

WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /ingestion .

FROM alpine:3.23

RUN apk --no-cache add ca-certificates poppler-utils font-dejavu tesseract-ocr tesseract-ocr-data-eng \
    && addgroup -S blix && adduser -S -G blix blix
COPY --from=build /ingestion /usr/local/bin/ingestion
USER blix
EXPOSE 9977
ENTRYPOINT ["ingestion"]
