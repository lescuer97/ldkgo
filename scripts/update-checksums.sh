#!/usr/bin/env bash
# Regenerate bindings/ldk_node_ffi/native/checksums.sha256 from the native
# libraries that are currently present on disk.
#
# Called automatically by generate-bindings.sh after building new libraries.
# Can also be run manually: ./scripts/update-checksums.sh
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
ROOT_DIR="$(cd "${SCRIPT_DIR}/.." && pwd)"
NATIVE_DIR="${ROOT_DIR}/bindings/ldk_node_ffi/native"
CHECKSUM_FILE="${NATIVE_DIR}/checksums.sha256"

if command -v sha256sum >/dev/null 2>&1; then
    HASH_CMD="sha256sum"
elif command -v shasum >/dev/null 2>&1; then
    HASH_CMD="shasum -a 256"
else
    echo "Neither sha256sum nor shasum found. Cannot generate checksums." >&2
    exit 1
fi

# Collect all native library files sorted for reproducibility.
LIBS=()
while IFS= read -r -d '' f; do
    LIBS+=("${f}")
done < <(find "${NATIVE_DIR}" -type f \( -name "*.so" -o -name "*.dylib" \) -print0 | sort -z)

if [ "${#LIBS[@]}" -eq 0 ]; then
    echo "No native libraries found under ${NATIVE_DIR}" >&2
    exit 1
fi

{
    for abs_path in "${LIBS[@]}"; do
        rel_path="${abs_path#"${NATIVE_DIR}/"}"
        hash="$(${HASH_CMD} "${abs_path}" | awk '{print $1}')"
        echo "${hash}  ${rel_path}"
    done
} > "${CHECKSUM_FILE}"

echo "Updated ${CHECKSUM_FILE}:"
cat "${CHECKSUM_FILE}"
