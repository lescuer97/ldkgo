#!/usr/bin/env bash
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
ROOT_DIR="$(cd "${SCRIPT_DIR}/.." && pwd)"
PACKAGE_DIR="${ROOT_DIR}/bindings/ldk_node_ffi"
HEADER_FILE="${PACKAGE_DIR}/ldk_node.h"
GO_FILE="${PACKAGE_DIR}/ldk_node.go"
LIB_FILE="${PACKAGE_DIR}/native/linux_amd64/libldk_node.so"
COMMIT_FILE="${PACKAGE_DIR}/LDK_NODE_COMMIT"

"${SCRIPT_DIR}/bootstrap-ldk-node.sh"

if [[ "$(uname -s)" != "Linux" ]]; then
    echo "Only Linux hosts are supported for this repository." >&2
    exit 1
fi

if [[ "$(uname -m)" != "x86_64" ]]; then
    echo "Only linux/amd64 is supported for this repository." >&2
    exit 1
fi

for required_file in "${HEADER_FILE}" "${GO_FILE}" "${LIB_FILE}"; do
    if [ ! -f "${required_file}" ]; then
        echo "Required file not found: ${required_file}" >&2
        exit 1
    fi
done

if [ ! -s "${COMMIT_FILE}" ]; then
    echo "LDK node commit marker is missing or empty: ${COMMIT_FILE}" >&2
    exit 1
fi

cat > "${PACKAGE_DIR}/link_linux_amd64.go" <<'EOF'
//go:build linux && amd64

package ldk_node

// #cgo LDFLAGS: -L${SRCDIR}/native/linux_amd64 -lldk_node -Wl,-rpath,${SRCDIR}/native/linux_amd64 -lm -ldl
import "C"
EOF

"${SCRIPT_DIR}/update-checksums.sh"

echo "Validated Linux-only LDK Node bindings in ${PACKAGE_DIR}"
