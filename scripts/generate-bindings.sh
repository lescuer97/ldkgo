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
else
    LIB_FILE="${LIB_DIR}/libcdk_ffi.so"
fi

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

mkdir -p "${PACKAGE_DIR}/native"
cp "${LIB_FILE}" "${PACKAGE_DIR}/native/"

git -C "${CDK_DIR}" rev-parse HEAD > "${PACKAGE_DIR}/CDK_COMMIT"

echo "Generated Go bindings in ${PACKAGE_DIR}"
