# --- build ---
# Native runner, cross-compiled to the target buildx asks for.
FROM --platform=$BUILDPLATFORM golang:1.26-alpine AS builder
ARG TARGETOS
ARG TARGETARCH
ARG VERSION=dev
WORKDIR /app
COPY go.mod ./
COPY *.go ./
COPY web/ web/
RUN CGO_ENABLED=0 GOOS=${TARGETOS} GOARCH=${TARGETARCH} \
    go build -ldflags="-s -w -X main.version=${VERSION}" -o apollo-audio-controls .

# --- runtime ---
# Pure Go with the UI embedded, so the image is just the binary.
FROM alpine:3
RUN apk add --no-cache ca-certificates tzdata \
 && addgroup -S app && adduser -S app -G app
WORKDIR /app
COPY --from=builder /app/apollo-audio-controls .
USER app
ENV PORT=8090
EXPOSE 8090
# Multicast on the local segment: run with --network host.
ENTRYPOINT ["./apollo-audio-controls"]
