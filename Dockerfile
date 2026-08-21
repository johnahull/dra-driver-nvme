FROM golang:1.26 AS builder
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -o /nvme-kubeletplugin ./cmd/nvme-kubeletplugin/

FROM registry.fedoraproject.org/fedora-minimal:latest
RUN microdnf install -y nvme-cli && microdnf clean all
COPY --from=builder /nvme-kubeletplugin /nvme-kubeletplugin
ENTRYPOINT ["/nvme-kubeletplugin"]
