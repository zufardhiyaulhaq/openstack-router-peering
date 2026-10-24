# Build on the runner's own platform and cross-compile, so multi-arch images need no emulation.
FROM --platform=$BUILDPLATFORM golang:1.25 AS build
ARG TARGETOS TARGETARCH
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH go build -trimpath -ldflags="-s -w" -o /router-peering ./cmd/router-peering

FROM gcr.io/distroless/static:nonroot
COPY --from=build /router-peering /router-peering
USER nonroot
ENTRYPOINT ["/router-peering"]
