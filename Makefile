BIN    ?= pix
PREFIX ?= $(HOME)/.local
GO     ?= go

.PHONY: all build x11 test vet install clean

# Fyne 2.8 builds both the Wayland and X11 GLFW backends into one binary
# and picks one at runtime, so `x11` is the same build.
all: build

VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
LDFLAGS ?= -s -w -X main.version=$(VERSION)

build:
	$(GO) build -ldflags '$(LDFLAGS)' -o $(BIN) .

x11: build

test:
	$(GO) test ./...

vet:
	$(GO) vet ./...

install: build
	install -d $(PREFIX)/bin
	install -m 0755 $(BIN) $(PREFIX)/bin/$(BIN)

clean:
	rm -f $(BIN)
