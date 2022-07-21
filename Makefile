via: via.go
	go build -buildmode=pie -ldflags="-s -w" -o $@ $<

.PHONY: install
install:
	install -D -m 755 via "${DESTDIR}/usr/bin/via"
	install -D -m 644 README.md "${DESTDIR}/usr/share/doc/via/README.md"
