#!/bin/bash
# Esegue il toolchain Go in un container: Go non e' installato sulla macchina.
#
# I due volumi di cache sono determinanti, non un'ottimizzazione: senza, ogni
# build riscarica i moduli e ricompila tutto da capo.
#
#   sh scripts/go.sh build ./...
#   sh scripts/go.sh test ./...

set -e
cd "$(dirname "$0")/.."

docker run --rm \
  -v "$PWD":/src -w /src \
  -v photovault-gomod:/go/pkg/mod \
  -v photovault-gobuild:/root/.cache/go-build \
  -e GOFLAGS=-buildvcs=false \
  golang:1.25-alpine go "$@"
