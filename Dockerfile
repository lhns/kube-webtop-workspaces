# Controller and gateway image (one binary, --mode selects).
FROM --platform=$BUILDPLATFORM golang:1.26 AS build
ARG TARGETOS TARGETARCH
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH \
    go build -trimpath -ldflags="-s -w" -o /out/kube-webtop-workspaces ./cmd/kube-webtop-workspaces

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /out/kube-webtop-workspaces /kube-webtop-workspaces
USER 65532:65532
ENTRYPOINT ["/kube-webtop-workspaces"]
