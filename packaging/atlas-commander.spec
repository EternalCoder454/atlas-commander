# RPM spec for Atlas Commander. Suitable for COPR: the build needs network access
# for the Go module cache, so enable it on the COPR project
# (`copr-cli modify --enable-net on`) or run rpmbuild with modules pre-fetched.

# No debuginfo subpackage. The binaries are linked with -s -w, which leaves no
# debug information to split out, and Fedora's rpmbuild then fails the whole
# build on an empty debugsourcefiles.list rather than skipping the subpackage.
%global debug_package %{nil}

Name:           atlas-commander
Version:        %{?_version}%{!?_version:0.1.0}
Release:        1%{?dist}
Summary:        Run, watch and govern a fleet of Claude agents
License:        MIT
URL:            https://github.com/EternalCoder454/atlas-commander
Source0:        %{url}/archive/v%{version}/%{name}-%{version}.tar.gz

BuildRequires:  golang >= 1.24
BuildRequires:  gcc-c++
BuildRequires:  pkgconfig(Qt6Widgets) >= 6.5
BuildRequires:  desktop-file-utils
Requires:       qt6-qtbase >= 6.5
Requires:       qt6-qtbase-gui >= 6.5
Requires:       qt6-qtsvg >= 6.5
# Commander drives the Claude Code CLI; it is not packaged here.
Suggests:       git

%description
A desktop application for running a fleet of Claude agents: a status board,
tool-use approvals, task queues, cost analytics and an append-only audit log,
written in Go with Qt 6 Widgets.

Approvals work through atlas-hook, a small helper that Claude Code runs as its
PreToolUse hook and that asks Commander over a local socket. It is installed
beside atlas-commander and the two must stay together.

%prep
%autosetup -n %{name}-%{version}

%build
export CGO_ENABLED=1
# Position-independent, as Fedora's packaging guidelines expect of every
# executable, so address-space randomisation applies to it; Go's default on
# linux/amd64 is a fixed-address binary, which rpmlint flags.
go build -buildmode=pie -trimpath -ldflags="-s -w" -o bin/ ./cmd/...

%install
install -Dm755 bin/atlas-commander       %{buildroot}%{_bindir}/atlas-commander
install -Dm755 bin/atlas-hook            %{buildroot}%{_bindir}/atlas-hook
install -Dm644 assets/icon.svg           %{buildroot}%{_datadir}/icons/hicolor/scalable/apps/com.atlas.Commander.svg
install -Dm644 assets/icon-16.svg        %{buildroot}%{_datadir}/icons/hicolor/16x16/apps/com.atlas.Commander.svg
install -Dm644 assets/icon-symbolic.svg  %{buildroot}%{_datadir}/icons/hicolor/symbolic/apps/com.atlas.Commander-symbolic.svg
install -d %{buildroot}%{_datadir}/applications
sed 's|@BIN@|%{_bindir}/atlas-commander|g' assets/com.atlas.Commander.desktop \
    > %{buildroot}%{_datadir}/applications/com.atlas.Commander.desktop

%check
desktop-file-validate %{buildroot}%{_datadir}/applications/com.atlas.Commander.desktop

%files
%license LICENSE NOTICE
%doc README.md CHANGELOG.md
%{_bindir}/atlas-commander
%{_bindir}/atlas-hook
%{_datadir}/applications/com.atlas.Commander.desktop
%{_datadir}/icons/hicolor/scalable/apps/com.atlas.Commander.svg
%{_datadir}/icons/hicolor/16x16/apps/com.atlas.Commander.svg
%{_datadir}/icons/hicolor/symbolic/apps/com.atlas.Commander-symbolic.svg

%changelog
* Wed Sep 30 2026 EternalHell <77252745+EternalCoder454@users.noreply.github.com> - 0.1.0-1
- First release
