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
rm -rf "${PACKAGE_DIR}"

if [[ "${OSTYPE:-}" == darwin* ]]; then
    LIB_FILE="${LIB_DIR}/libcdk_ffi.dylib"
    LIB_EXT="dylib"
    PLATFORM_OS="darwin"
else
    LIB_FILE="${LIB_DIR}/libcdk_ffi.so"
    LIB_EXT="so"
    PLATFORM_OS="linux"
fi

UNAME_ARCH="$(uname -m)"
case "${UNAME_ARCH}" in
    x86_64)
        PLATFORM_ARCH="amd64"
        ;;
    aarch64|arm64)
        PLATFORM_ARCH="arm64"
        ;;
    *)
        echo "Unsupported architecture for native artifact naming: ${UNAME_ARCH}" >&2
        exit 1
        ;;
esac

PLATFORM_DIR="${PACKAGE_DIR}/native/${PLATFORM_OS}_${PLATFORM_ARCH}"

pushd "${CDK_DIR}" >/dev/null
if [ "${BUILD_PROFILE}" = "release" ]; then
    cargo build --release --package cdk-ffi --features postgres
elif [ "${BUILD_PROFILE}" = "dev" ]; then
    cargo build --package cdk-ffi --features postgres
else
    cargo build --profile "${BUILD_PROFILE}" --package cdk-ffi --features postgres
fi
popd >/dev/null

if [ ! -f "${LIB_FILE}" ]; then
    echo "Expected library not found: ${LIB_FILE}" >&2
    exit 1
fi

pushd "${CDK_DIR}" >/dev/null
uniffi-bindgen-go "${LIB_FILE}" \
    --library \
    --config "${ROOT_DIR}/uniffi.toml" \
    --out-dir "${OUT_DIR}"
popd >/dev/null

mkdir -p "${PLATFORM_DIR}"
cp "${LIB_FILE}" "${PLATFORM_DIR}/libcdk_ffi.${LIB_EXT}"

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
