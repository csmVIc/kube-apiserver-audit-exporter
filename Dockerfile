FROM golang:1.24.0-alpine AS build

WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" \
    -o /out/kube-apiserver-audit-exporter ./cmd/kube-apiserver-audit-exporter

FROM scratch
COPY --from=build /out/kube-apiserver-audit-exporter /kube-apiserver-audit-exporter
ENTRYPOINT ["/kube-apiserver-audit-exporter"]
