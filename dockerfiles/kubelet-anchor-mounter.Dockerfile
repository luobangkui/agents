FROM --platform=$BUILDPLATFORM golang:1.25-alpine AS builder
ARG TARGETOS=linux
ARG TARGETARCH
WORKDIR /workspace
COPY go.mod go.sum ./
RUN go mod download
COPY cmd/kubelet-anchor-mounter/ cmd/kubelet-anchor-mounter/
RUN CGO_ENABLED=0 GOOS=${TARGETOS} GOARCH=${TARGETARCH} go build -trimpath -ldflags="-s -w" \
    -o /kubelet-anchor-mounter ./cmd/kubelet-anchor-mounter

FROM scratch
COPY --from=builder /kubelet-anchor-mounter /kubelet-anchor-mounter
ENTRYPOINT ["/kubelet-anchor-mounter"]
