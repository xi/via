via: via.go
	go build -o $@ $<

.PHONY: install
install:
	install -D -m 755 via "${DESTDIR}/usr/bin/via"
	install -D -m 644 via.service "${DESTDIR}/lib/systemd/system/via.service"
