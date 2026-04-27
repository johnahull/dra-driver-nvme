FROM golang:1.24 AS builder
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -o /nvme-kubeletplugin ./cmd/nvme-kubeletplugin/

FROM registry.access.redhat.com/ubi9/ubi-minimal:latest
COPY --from=builder /nvme-kubeletplugin /nvme-kubeletplugin
ENTRYPOINT ["/nvme-kubeletplugin"]
