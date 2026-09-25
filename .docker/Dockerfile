## STAGE 1 ##################################
FROM golang:1.25.14-alpine AS build

ENV CGO_ENABLED=0

WORKDIR /src
# go.sum con l'asterisco: questo servizio usa solo la libreria standard, quindi
# il file non esiste. Senza il glob la build fallirebbe.
COPY go.mod go.sum* ./
RUN go mod download

COPY cmd/ cmd/
COPY internal/ internal/
RUN go build -trimpath -ldflags="-s -w" -o /out/dedup ./cmd/dedup

## STAGE 2 ##################################
# Nessuna dipendenza esterna: si leggono solo JPEG (le thumbnail) e byte grezzi
# (per lo sha256), entrambi gestiti dalla libreria standard.
FROM alpine:3.21.8

RUN apk add --no-cache tzdata

COPY --from=build /out/dedup /usr/local/bin/dedup

USER 1000:1000
ENTRYPOINT ["/usr/local/bin/dedup"]
