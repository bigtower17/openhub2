# Cross-compile the hub binaries (ARMv7, static).
GOENV = GOOS=linux GOARCH=arm GOARM=7 CGO_ENABLED=0
FLAGS = -trimpath -ldflags "-s -w"

all: build/gatessh build/zbbridge build/ohweb

build/%: FORCE
	$(GOENV) go build $(FLAGS) -o $@ ./cmd/$*

clean:
	rm -rf build

.PHONY: all clean FORCE
FORCE:
