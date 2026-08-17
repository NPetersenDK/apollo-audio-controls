#!/usr/bin/env bash
# Build for this Mac.

set -e

cd "$(dirname "${BASH_SOURCE[0]}")"

GOOS=darwin GOARCH=arm64 CGO_ENABLED=0 go build -ldflags="-s -w" -o apollo-audio-controls .

echo "Built ./apollo-audio-controls - run it with ./apollo-audio-controls"
