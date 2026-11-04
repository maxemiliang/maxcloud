# The maxcloud-controller image. Built by .github/workflows/release-controller.yaml
# from this directory.
FROM golang:1.27.3-trixie AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
ARG VERSION=dev
RUN CGO_ENABLED=0 go build -trimpath -ldflags "-s -w -X main.Version=${VERSION}" \
      -o /maxcloud-controller ./cmd/maxcloud-controller

FROM gcr.io/distroless/static-debian13:nonroot
COPY --from=build /maxcloud-controller /maxcloud-controller
USER 65532:65532
ENTRYPOINT ["/maxcloud-controller"]
