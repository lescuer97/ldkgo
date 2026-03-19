#!/usr/bin/env bash
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
ROOT_DIR="$(cd "${SCRIPT_DIR}/.." && pwd)"

"${SCRIPT_DIR}/bootstrap-cdk.sh"
"${SCRIPT_DIR}/install-uniffi-bindgen-go.sh"

CDK_DIR="${ROOT_DIR}/.work/cdk"
BUILD_PROFILE="${BUILD_PROFILE:-release}"
LIB_DIR="${CDK_DIR}/target/${BUILD_PROFILE}"
OUT_DIR="${ROOT_DIR}/bindings"
PACKAGE_DIR="${OUT_DIR}/cdkffi"

mkdir -p "${OUT_DIR}"
# Preserve native libraries for other platforms when regenerating.
# Save them, wipe the directory, then restore.
NATIVE_BACKUP="$(mktemp -d)"
if [ -d "${PACKAGE_DIR}/native" ]; then
    cp -a "${PACKAGE_DIR}/native" "${NATIVE_BACKUP}/native"
fi
rm -rf "${PACKAGE_DIR}"

if [[ "${OSTYPE:-}" == darwin* ]]; then
    LIB_EXT="dylib"
    PLATFORM_OS="darwin"
else
    LIB_EXT="so"
    PLATFORM_OS="linux"
fi

# Allow overriding the target architecture for cross-compilation.
# E.g. TARGET_ARCH=x86_64 on an ARM Mac to produce a darwin_amd64 binary.
UNAME_ARCH="$(uname -m)"
BUILD_ARCH="${TARGET_ARCH:-${UNAME_ARCH}}"

case "${BUILD_ARCH}" in
    x86_64)  PLATFORM_ARCH="amd64" ;;
    aarch64|arm64) PLATFORM_ARCH="arm64" ;;
    *)
        echo "Unsupported architecture: ${BUILD_ARCH}" >&2
        exit 1
        ;;
esac

if [[ "${PLATFORM_OS}" == "darwin" ]]; then
    case "${PLATFORM_ARCH}" in
        amd64) RUST_TARGET_TRIPLE="x86_64-apple-darwin" ;;
        arm64) RUST_TARGET_TRIPLE="aarch64-apple-darwin" ;;
    esac
else
    RUST_TARGET_TRIPLE="x86_64-unknown-linux-gnu"
fi

# When cross-compiling, Rust puts output under target/<triple>/<profile>.
CROSS_COMPILING=false
if [ "${BUILD_ARCH}" != "${UNAME_ARCH}" ]; then
    CROSS_COMPILING=true
    LIB_DIR="${CDK_DIR}/target/${RUST_TARGET_TRIPLE}/${BUILD_PROFILE}"
fi

LIB_FILE="${LIB_DIR}/libcdk_ffi.${LIB_EXT}"
PLATFORM_DIR="${PACKAGE_DIR}/native/${PLATFORM_OS}_${PLATFORM_ARCH}"

# Build the Rust cdk-ffi library.
CARGO_ARGS=(--package cdk-ffi --features postgres)
if [ "${BUILD_PROFILE}" = "release" ]; then
    CARGO_ARGS+=(--release)
elif [ "${BUILD_PROFILE}" != "dev" ]; then
    CARGO_ARGS+=(--profile "${BUILD_PROFILE}")
fi
if [ "${CROSS_COMPILING}" = true ]; then
    CARGO_ARGS+=(--target "${RUST_TARGET_TRIPLE}")
fi

pushd "${CDK_DIR}" >/dev/null
cargo build "${CARGO_ARGS[@]}"
popd >/dev/null

if [ ! -f "${LIB_FILE}" ]; then
    echo "Expected library not found: ${LIB_FILE}" >&2
    exit 1
fi

# When SKIP_BINDGEN is set (e.g. cross-compilation), only produce the native
# library without generating Go source bindings or link files.
if [ "${SKIP_BINDGEN:-}" = "1" ]; then
    # Restore native libraries from other platforms.
    if [ -d "${NATIVE_BACKUP}/native" ]; then
        cp -a "${NATIVE_BACKUP}/native" "${PACKAGE_DIR}/native"
    fi
    rm -rf "${NATIVE_BACKUP}"

    mkdir -p "${PLATFORM_DIR}"
    cp "${LIB_FILE}" "${PLATFORM_DIR}/libcdk_ffi.${LIB_EXT}"
    if [[ "${PLATFORM_OS}" == "darwin" ]]; then
        install_name_tool -id "@rpath/libcdk_ffi.dylib" "${PLATFORM_DIR}/libcdk_ffi.dylib"
    fi
    echo "Built native library for ${PLATFORM_OS}_${PLATFORM_ARCH} (bindgen skipped)"
    exit 0
fi

pushd "${CDK_DIR}" >/dev/null
uniffi-bindgen-go "${LIB_FILE}" \
    --library \
    --config "${ROOT_DIR}/uniffi.toml" \
    --out-dir "${OUT_DIR}"
popd >/dev/null

# Restore native libraries from other platforms before copying the new one.
if [ -d "${NATIVE_BACKUP}/native" ]; then
    cp -a "${NATIVE_BACKUP}/native" "${PACKAGE_DIR}/native"
fi
rm -rf "${NATIVE_BACKUP}"

mkdir -p "${PLATFORM_DIR}"
cp "${LIB_FILE}" "${PLATFORM_DIR}/libcdk_ffi.${LIB_EXT}"
if [[ "${PLATFORM_OS}" == "darwin" ]]; then
    install_name_tool -id "@rpath/libcdk_ffi.dylib" "${PLATFORM_DIR}/libcdk_ffi.dylib"
fi

git -C "${CDK_DIR}" rev-parse HEAD > "${PACKAGE_DIR}/CDK_COMMIT"

cat > "${PACKAGE_DIR}/link_linux_amd64.go" <<'EOF'
//go:build linux && amd64

package cdk_ffi

// #cgo LDFLAGS: -L${SRCDIR}/native/linux_amd64 -lcdk_ffi -Wl,-rpath,${SRCDIR}/native/linux_amd64 -lm -ldl
import "C"
EOF

cat > "${PACKAGE_DIR}/link_darwin_amd64.go" <<'EOF'
//go:build darwin && amd64

package cdk_ffi

// #cgo LDFLAGS: -L${SRCDIR}/native/darwin_amd64 -lcdk_ffi -Wl,-rpath,${SRCDIR}/native/darwin_amd64 -lm
import "C"
EOF

cat > "${PACKAGE_DIR}/link_darwin_arm64.go" <<'EOF'
//go:build darwin && arm64

package cdk_ffi

// #cgo LDFLAGS: -L${SRCDIR}/native/darwin_arm64 -lcdk_ffi -Wl,-rpath,${SRCDIR}/native/darwin_arm64 -lm
import "C"
EOF

echo "Generated Go bindings in ${PACKAGE_DIR} (${PLATFORM_OS}_${PLATFORM_ARCH})"
