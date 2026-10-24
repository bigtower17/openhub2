# Cross-compile the hub binaries (ARMv7, static).
GOENV = GOOS=linux GOARCH=arm GOARM=7 CGO_ENABLED=0
FLAGS = -trimpath -ldflags "-s -w"

all: build/gatessh build/zbbridge build/ohweb

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
