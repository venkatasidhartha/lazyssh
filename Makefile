VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
LDFLAGS := -s -w -X main.version=$(VERSION)

.PHONY: build install test clean release
build:
	go build -ldflags "$(LDFLAGS)" -o lazyssh ./cmd/lazyssh
install:
	go install -ldflags "$(LDFLAGS)" ./cmd/lazyssh
test:
	go vet ./... && go test ./...
clean:
	rm -f lazyssh
release:
	scripts/build-release.sh
