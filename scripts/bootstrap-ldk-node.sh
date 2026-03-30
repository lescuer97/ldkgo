#!/usr/bin/env bash
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
ROOT_DIR="$(cd "${SCRIPT_DIR}/.." && pwd)"
PACKAGE_DIR="${ROOT_DIR}/bindings/ldk_node_ffi"

for required_file in \
    "${PACKAGE_DIR}/ldk_node.h" \
    "${PACKAGE_DIR}/ldk_node.go" \
    "${PACKAGE_DIR}/LDK_NODE_COMMIT" \
    "${PACKAGE_DIR}/native/linux_amd64/libldk_node.so"; do
    if [ ! -f "${required_file}" ]; then
        echo "Required LDK Node binding artifact not found: ${required_file}" >&2
        exit 1
    fi
done

echo "LDK Node binding artifacts are present in ${PACKAGE_DIR}"
