FROM golang:1.26 AS builder
WORKDIR /src
COPY . .
RUN CGO_ENABLED=0 go build -mod=vendor -o /nvme-kubeletplugin ./cmd/nvme-kubeletplugin/

FROM registry.access.redhat.com/ubi9/ubi-minimal:latest
COPY --from=builder /nvme-kubeletplugin /nvme-kubeletplugin
ENTRYPOINT ["/nvme-kubeletplugin"]
