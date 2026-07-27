# syntax=docker/dockerfile:1

# VARIANT selects the runtime base below: "prod" (default) is the real
# distroless nonroot image with no shell/utilities, unchanged from before.
# "dev" swaps in distroless's debug-nonroot variant, which bundles a busybox
# shell (docker exec ... sh) for local troubleshooting - not used for
# published prod images. Set via `--build-arg VARIANT=dev`; publish.yml
# passes it automatically based on whether the version being built is a
# "-dev" tag.
ARG VARIANT=prod

## --- Build stage ---
FROM golang:1.25-bookworm AS build
WORKDIR /src

ARG VERSION=dev

# Cache module downloads separately from source changes.
COPY go.mod go.sum* ./
RUN go mod download

COPY . .
RUN CGO_ENABLED=0 GOOS=linux go build -ldflags="-s -w -X main.Version=${VERSION}" -o /out/hhq ./cmd/server

## --- Runtime base variants ---
FROM gcr.io/distroless/static-debian12:nonroot AS runtime-prod
FROM gcr.io/distroless/static-debian12:debug-nonroot AS runtime-dev

## --- Runtime stage ---
FROM runtime-${VARIANT} AS runtime
WORKDIR /app

COPY --from=build /out/hhq ./hhq

# distroless "nonroot" image already runs as a non-root UID (65532); Kubernetes
# manifests additionally pin this via securityContext for clarity/defense-in-depth.
USER nonroot:nonroot

EXPOSE 8080

ENTRYPOINT ["/app/hhq"]
