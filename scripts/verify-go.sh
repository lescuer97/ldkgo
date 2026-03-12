#!/usr/bin/env bash
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
ROOT_DIR="$(cd "${SCRIPT_DIR}/.." && pwd)"
PACKAGE_DIR="${ROOT_DIR}/bindings/cdkffi"
NATIVE_DIR="${PACKAGE_DIR}/native"

if [ ! -f "${PACKAGE_DIR}/cdk_ffi.go" ]; then
    echo "Bindings not found. Run scripts/generate-bindings.sh first." >&2
    exit 1
fi

if [[ "${OSTYPE:-}" == darwin* ]]; then
    export DYLD_LIBRARY_PATH="${NATIVE_DIR}:${DYLD_LIBRARY_PATH:-}"
    export CGO_LDFLAGS="-L${NATIVE_DIR} -lcdk_ffi -lm"
else
    export LD_LIBRARY_PATH="${NATIVE_DIR}:${LD_LIBRARY_PATH:-}"
    export CGO_LDFLAGS="-L${NATIVE_DIR} -lcdk_ffi -lm -ldl"
fi

export CGO_ENABLED=1

pushd "${ROOT_DIR}" >/dev/null
go test ./bindings/cdkffi
popd >/dev/null

echo "Go verification passed"
