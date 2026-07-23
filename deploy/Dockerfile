# syntax=docker/dockerfile:1

## --- Build stage ---
FROM golang:1.25-bookworm AS build
WORKDIR /src

ARG VERSION=dev

# Cache module downloads separately from source changes.
COPY go.mod go.sum* ./
RUN go mod download

COPY . .
RUN CGO_ENABLED=0 GOOS=linux go build -ldflags="-s -w -X main.Version=${VERSION}" -o /out/hhq ./cmd/server

## --- Runtime stage ---
FROM gcr.io/distroless/static-debian12:nonroot AS runtime
WORKDIR /app

COPY --from=build /out/hhq ./hhq

# distroless "nonroot" image already runs as a non-root UID (65532); Kubernetes
# manifests additionally pin this via securityContext for clarity/defense-in-depth.
USER nonroot:nonroot

EXPOSE 8080

ENTRYPOINT ["/app/hhq"]
