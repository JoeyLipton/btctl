PREFIX ?= /usr/local
BINARY  = btctl

.PHONY: build install uninstall clean

build:
	go build -o $(BINARY) .

install: build
	install -Dm755 $(BINARY) $(DESTDIR)$(PREFIX)/bin/$(BINARY)

uninstall:
	rm -f $(DESTDIR)$(PREFIX)/bin/$(BINARY)

clean:
	rm -f $(BINARY)
