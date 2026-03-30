#!/usr/bin/env bash
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
ROOT_DIR="$(cd "${SCRIPT_DIR}/.." && pwd)"
PACKAGE_DIR="${ROOT_DIR}/bindings/ldk_node_ffi"

if [ ! -f "${PACKAGE_DIR}/ldk_node.go" ]; then
    echo "Bindings not found. Run scripts/generate-bindings.sh first." >&2
    exit 1
fi

# Verify integrity of native libraries before running any Go code.
"${SCRIPT_DIR}/verify-checksums.sh"

export CGO_ENABLED=1

pushd "${ROOT_DIR}" >/dev/null
go test ./bindings/ldk_node_ffi
popd >/dev/null

echo "Go verification passed"
