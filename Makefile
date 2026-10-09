BUF := go run github.com/bufbuild/buf/cmd/buf@v1.72.0

.PHONY: generate lint-proto build test openvpn app dist verify test-packaging \
	go-daemon go-test linux-build linux-test linux-root-test test-packaging-linux deb verify-deb install

# Regenerates internal/gen and macos/Sources/PlaitwayAPI from proto/. The
# generated files are committed, so a normal build needs neither this target
# nor buf. The two Swift plugins are built from the versions pinned in
# macos/Package.resolved (one invocation each: --product only honours the last).
generate:
	swift build -c release --package-path macos --product protoc-gen-swift
	swift build -c release --package-path macos --product protoc-gen-grpc-swift-2
	$(BUF) generate

# buf lint with the buf version pinned above.
lint-proto:
	$(BUF) lint

build:
	go build -o bin/plaitwayd ./cmd/plaitwayd
	swift build --package-path macos

# The Swift tests start the daemon binary named by PLAITWAY_DAEMON.
test: build
	go test ./...
	PLAITWAY_DAEMON=$(CURDIR)/bin/plaitwayd swift test --package-path macos

# Packaging (packaging/, scripts/verify-bundle.sh). `make app` and `make dist`
# sign with the Developer ID identity in PLAITWAY_SIGN_IDENTITY (packaging/README.md).
# The bundled openvpn is rebuilt only when its recipe changes: the three
# prerequisites are exactly what packaging/build-openvpn.sh reads from the repository.
openvpn: build/openvpn/bin/openvpn

build/openvpn/bin/openvpn: packaging/build-openvpn.sh packaging/lib.sh packaging/openvpn-deps.env
	packaging/build-openvpn.sh

# `app` is phony, so every `make app` and `make dist` assembles and signs the
# bundle again; packaging/package-app.sh deletes the zip and dmg of the app it
# replaces, so a dist file never outlives the build it was made from.
app: openvpn
	packaging/package-app.sh

dist: app
	packaging/make-dist.sh

verify:
	scripts/verify-bundle.sh

# The scripts against fakes (codesign, launchctl) and a small signed app, and
# the Go tests of the notices generator.
test-packaging:
	packaging/lib_test.sh
	go test -race -count=1 ./packaging/...

# Linux (packaging/linux, scripts/linux). None of these needs Swift.
# go-daemon and go-test are the Go-only part of build and test.
go-daemon:
	go build -o bin/plaitwayd ./cmd/plaitwayd

go-test:
	go test ./...

# The programs, the notices and the changelog in build/linux, which `deb`,
# `install` and scripts/linux/dev-install-daemon.sh lay out.
linux-build:
	packaging/linux/build.sh

# The Go tests, then the Python tests of the GTK app (linux/README.md says what
# they need: PyGObject, GTK 4, libadwaita, grpcio, protobuf; a test that needs
# what the machine lacks, such as a display, skips and says why).
linux-test: go-daemon go-test
	PLAITWAY_DAEMON=$(CURDIR)/bin/plaitwayd PYTHONPATH=linux/src python3 -m unittest discover -s linux/tests

# The tests that change routes, links and DNS settings, each in a private user
# and network namespace (scripts/linux/root-tests.sh). They need no sudo.
linux-root-test:
	scripts/linux/root-tests.sh

# The scripts against fakes (systemctl, install, the Debian tools), the apt
# repository script with a real apt, and the Go tests of the notices generator.
test-packaging-linux:
	packaging/linux/lib_test.sh
	packaging/linux/apt_repo_test.sh
	go test -race -count=1 ./packaging/...

# build/linux/plaitway_<version>_<arch>.deb, for the architecture of this machine.
deb:
	packaging/linux/build-deb.sh

verify-deb:
	packaging/linux/verify-deb.sh

# Lays out the files of `make linux-build` below DESTDIR and PREFIX, and starts
# nothing. As root: make linux-build, then sudo make install. The Debian package
# does the rest in its maintainer scripts, and so must you; the target says what.
PREFIX ?= /usr/local
DESTDIR ?=
install:
	packaging/linux/install.sh --prefix "$(PREFIX)" --destdir "$(DESTDIR)"
	@echo "Now: systemctl daemon-reload && systemctl enable --now plaitwayd.service"
	@echo "     update-mime-database $(PREFIX)/share/mime; gtk-update-icon-cache -t $(PREFIX)/share/icons/hicolor; update-desktop-database $(PREFIX)/share/applications"
