# Go builds with -trimpath omit build-directory source paths from DWARF info,
# which leaves debugsourcefiles.list empty on older rpm-build (e.g. RHEL 8).
%undefine _debugsource_packages

Name:           wait-dns-online
Version:        0.1.0
Release:        1%{?dist}
Summary:        Wait for DNS resolution before mounting remote filesystems
License:        MIT
URL:            https://github.com/trbecker/wait-dns-online
Source0:        %{url}/archive/v%{version}/%{name}-%{version}.tar.gz

BuildRequires:  golang >= 1.21
BuildRequires:  systemd-rpm-macros
%{?systemd_requires}

%description
wait-dns-online is a lightweight CLI utility and systemd oneshot service that
blocks until one or more target hostnames resolve via the system resolver
(libc/NSS getaddrinfo). It orders remote filesystem mounts (nfs, nfs4, cifs,
smb3, smbfs) in /etc/fstab after working DNS resolution is available during
system boot.

%prep
%autosetup -n %{name}-%{version}

%build
export CGO_CFLAGS="%{optflags}"
export CGO_LDFLAGS="%{build_ldflags}"
go build -v -trimpath -tags netcgo -buildmode=pie -o %{name} .

%install
install -D -m 0755 %{name} %{buildroot}%{_bindir}/%{name}
install -D -m 0644 systemd/%{name}.service %{buildroot}%{_unitdir}/%{name}.service
install -D -m 0644 systemd/%{name}.default %{buildroot}%{_sysconfdir}/default/%{name}

%check
go test -v ./...

%post
%systemd_post %{name}.service

%preun
%systemd_preun %{name}.service

%postun
%systemd_postun_with_restart %{name}.service

%files
%license LICENSE
%doc README.md
%{_bindir}/%{name}
%{_unitdir}/%{name}.service
%config(noreplace) %{_sysconfdir}/default/%{name}

%changelog
* Wed Nov 18 2026 Thiago Becker <tbecker@redhat.com> - 0.1.0-1
- Initial Fedora RPM package
