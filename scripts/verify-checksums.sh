#!/usr/bin/env bash
# Verify the SHA-256 integrity of committed native libraries.
# Run this locally or in CI to confirm that the blobs in git have not been
# tampered with since they were generated.
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
ROOT_DIR="$(cd "${SCRIPT_DIR}/.." && pwd)"
NATIVE_DIR="${ROOT_DIR}/bindings/ldk_node_ffi/native"
CHECKSUM_FILE="${NATIVE_DIR}/checksums.sha256"

if [ ! -f "${CHECKSUM_FILE}" ]; then
    echo "Checksum file not found: ${CHECKSUM_FILE}" >&2
    exit 1
fi

echo "Verifying native library checksums..."

FAILED=0
while IFS= read -r line || [ -n "${line}" ]; do
    # Skip blank lines and comments
    [[ -z "${line}" || "${line}" =~ ^# ]] && continue

    expected_hash="${line%% *}"
    rel_path="${line##* }"
    abs_path="${NATIVE_DIR}/${rel_path}"

    if [ ! -f "${abs_path}" ]; then
        # Missing libraries are only a warning; some platforms may not be
        # committed on every build.
        echo "  SKIP  ${rel_path} (file not present)"
        continue
    fi

    if command -v sha256sum >/dev/null 2>&1; then
        actual_hash="$(sha256sum "${abs_path}" | awk '{print $1}')"
    elif command -v shasum >/dev/null 2>&1; then
        actual_hash="$(shasum -a 256 "${abs_path}" | awk '{print $1}')"
    else
        echo "Neither sha256sum nor shasum found. Cannot verify checksums." >&2
        exit 1
    fi

    if [ "${actual_hash}" = "${expected_hash}" ]; then
        echo "  OK    ${rel_path}"
    else
        echo "  FAIL  ${rel_path}" >&2
        echo "        expected: ${expected_hash}" >&2
        echo "        actual:   ${actual_hash}" >&2
        FAILED=$((FAILED + 1))
    fi
done < "${CHECKSUM_FILE}"

if [ "${FAILED}" -gt 0 ]; then
    echo ""
    echo "ERROR: ${FAILED} native library checksum(s) failed." >&2
    echo "The committed binary blobs do not match their recorded SHA-256 hashes." >&2
    echo "Regenerate the bindings (make generate) to update checksums, or" >&2
    echo "investigate whether the libraries have been tampered with." >&2
    exit 1
fi

echo "All checksums verified."
