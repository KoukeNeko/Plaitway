# Copyright 2026 KoukeNeko
# Distributed under the terms of the MIT License

EAPI=8

PYTHON_COMPAT=( python3_{12..14} )

inherit git-r3 go-module python-single-r1 xdg

DESCRIPTION="Several OpenVPN and WireGuard profiles at once, with a privileged helper"
HOMEPAGE="https://github.com/KoukeNeko/Plaitway"
EGIT_REPO_URI="https://github.com/KoukeNeko/Plaitway.git"

LICENSE="MIT BSD Apache-2.0"
SLOT="0"
IUSE="systemd"
REQUIRED_USE="${PYTHON_REQUIRED_USE}"

# The Go programs link the C library; they carry no other library of the system.
# OpenVPN profiles need openvpn 2.6 or later. DNS settings go through
# systemd-resolved, or through openresolv where there is no systemd, for a full
# tunnel only (see the README of the project).
RDEPEND="
	${PYTHON_DEPS}
	>=net-vpn/openvpn-2.6
	!systemd? (
		app-admin/logrotate
		net-dns/openresolv
	)
	systemd? ( sys-auth/polkit )
	>=gui-libs/gtk-4.14:4[introspection]
	>=gui-libs/libadwaita-1.5:1[introspection]
	>=app-crypt/libsecret-0.20[introspection]
	$(python_gen_cond_dep '
		dev-python/grpcio[${PYTHON_USEDEP}]
		dev-python/protobuf[${PYTHON_USEDEP}]
		dev-python/pygobject:3[${PYTHON_USEDEP}]
	')
"
BDEPEND+=" >=dev-lang/go-1.27.1:="

# The programs are built and stripped by packaging/linux/build.sh.
QA_PRESTRIPPED="usr/bin/plaitway usr/libexec/plaitway/plaitwayd"

src_unpack() {
	git-r3_src_unpack
	# The build runs without a network: the modules come from the cache of the
	# eclass, which packaging/notices also reads for the licenses.
	cd "${S}" || die
	ego mod download
}

src_compile() {
	GOFLAGS="-mod=mod -modcacherw" GOPROXY=off packaging/linux/build.sh || die
}

src_install() {
	local args=( --prefix /usr --destdir "${D}" --python-dir "$(python_get_sitedir)" )
	use systemd || args+=( --openrc )

	packaging/linux/install.sh "${args[@]}" || die
	use systemd || rm -r "${ED}"/usr/lib/systemd || die

	# Portage keeps the documents in a directory of the package and compresses
	# them itself.
	mv "${ED}"/usr/share/doc/plaitway "${ED}/usr/share/doc/${PF}" || die
	gzip -d "${ED}/usr/share/doc/${PF}"/changelog.gz || die

	python_fix_shebang "${ED}"/usr/bin/plaitway-app
	python_optimize
}

pkg_postinst() {
	xdg_pkg_postinst

	if use systemd; then
		elog "Start the helper with: systemctl enable --now plaitwayd"
	else
		elog "Start the helper with: rc-service plaitwayd start"
		elog "and at boot with: rc-update add plaitwayd default"
		elog "DNS settings go through resolvconf, for a full tunnel only."
	fi
}
