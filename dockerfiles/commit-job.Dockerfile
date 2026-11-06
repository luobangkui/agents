# Build the commit-job binary + commit-rebase helper
FROM golang:1.25 AS builder
ARG TARGETOS
ARG TARGETARCH
ARG VERSION
ARG NERDCTL_BRANCH

WORKDIR /workspace
COPY go.mod go.mod
COPY go.sum go.sum
COPY cmd/commit-job/ cmd/commit-job/
COPY api api/
COPY pkg pkg/
COPY client client/

RUN CGO_ENABLED=0 GOOS=${TARGETOS:-linux} GOARCH=${TARGETARCH} \
    go build -ldflags "-X main.version=${VERSION}" -a -o commit-job ./cmd/commit-job

# Isolated rebase helper (separate go.mod so agents k8s deps stay untouched).
# Use committed go.sum (-mod=readonly) for reproducible CI builds.
COPY tools/commit-rebase/ tools/commit-rebase/
WORKDIR /workspace/tools/commit-rebase
RUN CGO_ENABLED=0 GOOS=${TARGETOS:-linux} GOARCH=${TARGETARCH} \
    go build -mod=readonly -a -o /workspace/commit-rebase .

WORKDIR /workspace/nerdctl-builder
RUN git clone -b ${NERDCTL_BRANCH:-v2.0.0} https://github.com/containerd/nerdctl.git
RUN cd nerdctl && CGO_ENABLED=0 GOOS=${TARGETOS:-linux} GOARCH=${TARGETARCH} make

FROM alpine:3.20
WORKDIR /

COPY --from=builder /workspace/commit-job .
COPY --from=builder /workspace/commit-rebase /commit-rebase
COPY --from=builder /workspace/nerdctl-builder/nerdctl/_output/nerdctl /usr/bin/nerdctl

ENTRYPOINT ["/commit-job"]
