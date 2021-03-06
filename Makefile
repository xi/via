via: via.go
	go build -buildmode=pie -ldflags="-s -w" -o $@ $<

.PHONY: install
install:
	install -D -m 755 via "${DESTDIR}/usr/bin/via"
	install -D -m 644 via.service "${DESTDIR}/lib/systemd/system/via.service"
	install -D -m 755 via-cleanup "${DESTDIR}/etc/cron.daily/via-cleanup"
	install -D -m 644 via.nginx "${DESTDIR}/etc/nginx/sites-available/via"
	install -D -m 644 README.md "${DESTDIR}/usr/share/doc/via/README.md"
