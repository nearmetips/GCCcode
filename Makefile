BINARY := bin/gcccode

.PHONY: build cli test fmt

build:
	mkdir -p bin
	CGO_ENABLED=1 go build -o $(BINARY) ./cmd/gcccode

cli:
	mkdir -p bin
	CGO_ENABLED=0 go build -tags nogui -o $(BINARY) ./cmd/gcccode

test:
	go test -tags nogui ./...

fmt:
	gofmt -w ./cmd ./internal
