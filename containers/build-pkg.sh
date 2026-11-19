#!/bin/bash
set -euo pipefail

SRC_DIR="${SRC_DIR:-/src}"
OUT_DIR="${OUT_DIR:-/out}"

if [ ! -f "${SRC_DIR}/dns-online.spec" ]; then
    echo "Error: ${SRC_DIR}/dns-online.spec not found. Mount the project source directory to ${SRC_DIR} (e.g. -v .:/src:ro,Z)." >&2
    exit 1
fi

VERSION="${VERSION:-$(awk '/^Version:/ {print $2}' "${SRC_DIR}/dns-online.spec")}"
WORK_DIR="$(mktemp -d)"
RPM_TOPDIR="${WORK_DIR}/rpmbuild"
trap 'rm -rf "${WORK_DIR}"' EXIT

mkdir -p "${RPM_TOPDIR}/BUILD" \
         "${RPM_TOPDIR}/RPMS" \
         "${RPM_TOPDIR}/SOURCES" \
         "${RPM_TOPDIR}/SPECS" \
         "${RPM_TOPDIR}/SRPMS" \
         "${OUT_DIR}"

# Copy source to an isolated build tree so we don't dirty the bind-mounted /src
cp -a "${SRC_DIR}" "${WORK_DIR}/dns-online-${VERSION}"
cd "${WORK_DIR}/dns-online-${VERSION}"

make clean

# Run unit tests with race detection and coverage, saving logs and coverage reports
go test -v -race -covermode=atomic -coverprofile="${OUT_DIR}/coverage.out" ./... 2>&1 | tee "${OUT_DIR}/test.log"
go tool cover -func="${OUT_DIR}/coverage.out" 2>&1 | tee "${OUT_DIR}/coverage.txt"
go tool cover -html="${OUT_DIR}/coverage.out" -o "${OUT_DIR}/coverage.html"

# Run integration tests (compiled netcgo binary, live UDP DNS server, systemd ordering), saving results
go test -v -race -tags "netcgo integration" -run '^TestIntegration' ./... 2>&1 | tee "${OUT_DIR}/integration-test.log"

# Build binary and RPMs, saving full build log
{
    echo "=== Building dns-online binary ==="
    make build

    echo "=== Creating source tarball ==="
    tar -C "${WORK_DIR}" -czf "${RPM_TOPDIR}/SOURCES/dns-online-${VERSION}.tar.gz" \
        --exclude='.git' \
        --exclude='dist' \
        --exclude='*.rpm' \
        --exclude='*.out' \
        --exclude='coverage.*' \
        --exclude='rhsm.env' \
        --exclude="dns-online-${VERSION}/dns-online" \
        "dns-online-${VERSION}"

    echo "=== Building RPM and SRPM packages ==="
    rpmbuild -ba --define "_topdir ${RPM_TOPDIR}" dns-online.spec
} 2>&1 | tee "${OUT_DIR}/build.log"

cp -v "${WORK_DIR}/dns-online-${VERSION}/dns-online" \
      "${RPM_TOPDIR}"/RPMS/*/*.rpm \
      "${RPM_TOPDIR}"/SRPMS/*.rpm \
      "${OUT_DIR}/"
