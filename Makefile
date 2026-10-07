BUF := go run github.com/bufbuild/buf/cmd/buf@v1.72.0

.PHONY: generate build test openvpn app dist verify test-packaging

# Regenerates internal/gen and macos/Sources/PlaitwayAPI from proto/. The
# generated files are committed, so a normal build needs neither this target
# nor buf. The two Swift plugins are built from the versions pinned in
# macos/Package.resolved (one invocation each: --product only honours the last).
generate:
	swift build -c release --package-path macos --product protoc-gen-swift
	swift build -c release --package-path macos --product protoc-gen-grpc-swift-2
	$(BUF) generate

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
