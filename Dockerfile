FROM golang:1.22-alpine AS build
ARG VERSION=dev
WORKDIR /src
COPY go.mod go.sum* ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -trimpath -ldflags "-s -w -X main.version=${VERSION}" -o /llmux ./cmd/llmux

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /llmux /llmux
COPY configs/llmux.example.yaml /etc/llmux/llmux.yaml
EXPOSE 8080
ENTRYPOINT ["/llmux", "--config", "/etc/llmux/llmux.yaml"]
