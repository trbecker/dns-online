BINARY      ?= dns-online
PREFIX      ?= /usr
BINDIR      ?= $(PREFIX)/bin
SYSTEMDDIR  ?= /etc/systemd/system
DEFAULTDIR  ?= /etc/default
GO          ?= go
GOFLAGS     ?= -trimpath -tags netcgo
LDFLAGS     ?= -s -w

.PHONY: all build build-static test test-integration clean install uninstall

all: build

build:
	$(GO) build $(GOFLAGS) -ldflags "$(LDFLAGS)" -o $(BINARY) .

build-static:
	CGO_ENABLED=0 $(GO) build -trimpath -ldflags "$(LDFLAGS)" -o $(BINARY) .

test:
	$(GO) test -v -race ./...

test-integration:
	$(GO) test -v -race -tags "netcgo integration" -run '^TestIntegration' ./...

clean:
	rm -f $(BINARY)

install: build
	install -d $(DESTDIR)$(BINDIR)
	install -m 0755 $(BINARY) $(DESTDIR)$(BINDIR)/$(BINARY)
	install -d $(DESTDIR)$(SYSTEMDDIR)
	install -m 0644 systemd/dns-online.service $(DESTDIR)$(SYSTEMDDIR)/dns-online.service
	install -d $(DESTDIR)$(DEFAULTDIR)
	@if [ ! -f $(DESTDIR)$(DEFAULTDIR)/dns-online ]; then \
		install -m 0644 systemd/dns-online.default $(DESTDIR)$(DEFAULTDIR)/dns-online; \
	else \
		echo "$(DESTDIR)$(DEFAULTDIR)/dns-online already exists, skipping overwrite"; \
	fi

uninstall:
	rm -f $(DESTDIR)$(BINDIR)/$(BINARY)
	rm -f $(DESTDIR)$(SYSTEMDDIR)/dns-online.service
