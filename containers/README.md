# Obtaining and Using Red Hat Activation Keys for Container Builds

This guide explains how to create a Red Hat Subscription Management (RHSM) activation key, find your Organization ID, build the reusable builder containers ([Containerfile.rhel8](Containerfile.rhel8), [Containerfile.rhel9](Containerfile.rhel9), [Containerfile.rhel10](Containerfile.rhel10), [Containerfile.fedora44](Containerfile.fedora44), [Containerfile.fedora45](Containerfile.fedora45)), and run them to package `dns-online`.

> **Note:** When building on a host that is already registered with `subscription-manager`, Podman automatically mounts host entitlements (`/etc/pki/entitlement` and `/etc/rhsm`) into the container build environment. You only need an activation key when building the RHEL images on unsubscribed hosts (such as Fedora, macOS, Windows, or CI runners).

---

## Prerequisites: Red Hat Account & Subscription

You need a Red Hat account with an active RHEL subscription. If you do not have one, you can register for a free **Red Hat Developer Subscription for Individuals** (which includes access to RHEL 8, 9, and 10 repositories):

1. Go to <https://developers.redhat.com/register> and create or sign in to your account.
2. Accept the Red Hat Developer Program terms to activate your no-cost developer subscription.

---

## Step 1: Create an Activation Key in the Red Hat Hybrid Cloud Console

Red Hat manages activation keys under Simple Content Access (SCA) in the **Red Hat Hybrid Cloud Console**:

1. Log in to the Red Hat Hybrid Cloud Console:
   <https://console.redhat.com/insights/connector/activation-keys>
   *(Alternatively, navigate to **Red Hat Insights** -> **RHEL** -> **Inventory** -> **System Configuration** -> **Activation Keys**, or **Subscription Inventory** -> **Activation Keys**).*
2. Click **Create activation key**.
3. Fill in the details:
   - **Name**: Enter a descriptive name (e.g., `container-builds`). *(Avoid spaces or special characters).*
   - **Workload / Release version**: Leave as default / unrestricted so the same key can be used across RHEL 8, RHEL 9, and RHEL 10 builds.
   - **Role / SLA / Usage**: Optional (e.g., `Development/Test`).
4. Click **Create**.

---

## Step 2: Find Your Organization ID (`RHSM_ORG`)

To register with an activation key, `subscription-manager` requires both the **Activation Key Name** and your numeric **Organization ID**.

You can find your Organization ID in any of the following ways:

### Option A: From the Hybrid Cloud Console UI
- On the **Activation Keys** page (<https://console.redhat.com/insights/connector/activation-keys>), your **Organization ID** is displayed above the activation keys table.
- Or click your profile avatar in the top-right corner of <https://console.redhat.com>; the dropdown lists your **Org ID**.

### Option B: From an Already-Registered RHEL System
Run the following command on any registered RHEL machine:
```bash
sudo subscription-manager identity
```
Look for the `org ID:` line in the output.

---

## Step 3: Build the Reusable Builder Images (Once)

Each `Containerfile` builds a reusable builder image containing the Go toolchain, `gcc`, `rpm-build`, `systemd-rpm-macros`, and the entrypoint script [build-pkg.sh](build-pkg.sh).

### 1. Create a Secret Environment File (For RHEL builds on unsubscribed hosts)

Create a local `rhsm.env` file in the repository root (ignored by Git via `.gitignore`):

```ini
RHSM_ORG=12345678
RHSM_ACTIVATIONKEY=container-builds
```

```bash
chmod 600 rhsm.env
```

### 2. Build the Builder Images

Pass `rhsm.env` via `--secret id=rhsm,src=rhsm.env` for the RHEL images so your credentials are mounted only in memory during the `dnf install` step and automatically unregistered before the layer finishes:

```bash
# RHEL 8, 9, 10
podman build --secret id=rhsm,src=rhsm.env -f containers/Containerfile.rhel8 -t dns-online-builder:rhel8 .
podman build --secret id=rhsm,src=rhsm.env -f containers/Containerfile.rhel9 -t dns-online-builder:rhel9 .
podman build --secret id=rhsm,src=rhsm.env -f containers/Containerfile.rhel10 -t dns-online-builder:rhel10 .

# Fedora 44, 45 (no registration needed)
podman build -f containers/Containerfile.fedora44 -t dns-online-builder:fedora44 .
podman build -f containers/Containerfile.fedora45 -t dns-online-builder:fedora45 .
```

---

## Step 4: Execute the Containers to Build Packages

Once a builder image is built, run it anytime by mounting the project source directory to `/src` and an output directory to `/out`. The container copies the source to an isolated temporary build tree, runs the unit tests with race detection and coverage, builds the binary and RPM/SRPM packages, and writes all artifacts and logs to `/out`:

- `dns-online`: compiled binary
- `*.rpm` / `*.src.rpm`: binary, debuginfo, and source RPM packages
- `build.log`: combined binary build and `rpmbuild` log
- `test.log`: verbose unit test log (`go test -v -race`)
- `integration-test.log`: verbose integration test log (`go test -v -race -tags "netcgo integration"`)
- `coverage.out`: raw Go coverage profile
- `coverage.txt`: per-function coverage summary (`go tool cover -func`)
- `coverage.html`: HTML coverage report (`go tool cover -html`)

```bash
for target in rhel8 rhel9 rhel10 fedora44 fedora45; do
    mkdir -p "dist/${target}"
    podman run --rm \
        -v .:/src:ro,Z \
        -v "./dist/${target}:/out:Z" \
        "dns-online-builder:${target}"
done
```
