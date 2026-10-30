VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
LDFLAGS := -s -w -X main.version=$(VERSION)

.PHONY: build build-cgo run tui test tidy fmt docker clean

build:
	CGO_ENABLED=0 go build -trimpath -ldflags "$(LDFLAGS)" -o bin/llmux ./cmd/llmux

build-cgo:
	CGO_ENABLED=1 go build -tags mattn -trimpath -ldflags "$(LDFLAGS)" -o bin/llmux ./cmd/llmux

run: build
	./bin/llmux --config configs/llmux.example.yaml

tui: build
	./bin/llmux --config configs/llmux.example.yaml --tui

test:
	go vet ./...
	go test -race ./...

tidy:
	go mod tidy

fmt:
	gofmt -w .

docker:
	docker build -t llmux:$(VERSION) --build-arg VERSION=$(VERSION) .

clean:
	rm -rf bin
