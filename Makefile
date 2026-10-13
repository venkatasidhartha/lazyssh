VERSION ?= v0.1.0
LDFLAGS := -s -w -X main.version=$(VERSION)

.PHONY: build install test clean
build:
	go build -ldflags "$(LDFLAGS)" -o hopssh ./cmd/hopssh
install:
	go install -ldflags "$(LDFLAGS)" ./cmd/hopssh
test:
	go vet ./... && go test ./...
clean:
	rm -f hopssh
