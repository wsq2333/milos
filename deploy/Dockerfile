# syntax=docker/dockerfile:1
FROM --platform=$BUILDPLATFORM golang:1.26.5 AS build
ARG TARGETOS=linux
ARG TARGETARCH=amd64
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY api/ api/
COPY cmd/ cmd/
COPY internal/ internal/
RUN CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH go build -trimpath -ldflags="-s -w" -o /out/milos-controller ./cmd/controller

FROM gcr.io/distroless/static:nonroot
COPY --from=build /out/milos-controller /milos-controller
USER 65532:65532
ENTRYPOINT ["/milos-controller"]
