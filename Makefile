BINARY := bin/gcccode

.PHONY: build cli test fmt app dmg icon

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

# macOS 桌面应用：dist/GCC Code.app（通用二进制，自签名）
app:
	scripts/build-app.sh

dmg:
	scripts/build-app.sh --dmg

# 重新生成应用图标（需要 Pillow）
icon:
	python3 packaging/macos/icon.py
