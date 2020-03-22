pkgname='via'
pkgdesc='Simple pubsub server'
arch=('amd64')
url='https://github.com/xi/via'
license='MIT'

package() {
	make
	make DESTDIR="$pkgdir" install
}
