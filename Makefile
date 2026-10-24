# Cross-compile the hub binaries (ARMv7, static).
GOENV = GOOS=linux GOARCH=arm GOARM=7 CGO_ENABLED=0
VERSION := $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
FLAGS = -trimpath -ldflags "-s -w -X main.version=$(VERSION)"

all: build/gatessh build/zbbridge build/ohweb build/btproxy

build/%: FORCE
	$(GOENV) go build $(FLAGS) -o $@ ./cmd/$*

# ohinstall runs on the user's PC, not on the hub.
ohinstall: build/ohinstall.exe

build/ohinstall.exe: FORCE
	CGO_ENABLED=0 go build $(FLAGS) -o $@ ./cmd/ohinstall

clean:
	rm -rf build

.PHONY: all ohinstall clean FORCE
FORCE:
