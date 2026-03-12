#!/usr/bin/env bash
set -euo pipefail

UNIFFI_BINDGEN_GO_TAG="${UNIFFI_BINDGEN_GO_TAG:-v0.5.0+v0.29.5}"
UNIFFI_BINDGEN_GO_REPO="${UNIFFI_BINDGEN_GO_REPO:-https://github.com/NordSecurity/uniffi-bindgen-go}"

if command -v uniffi-bindgen-go >/dev/null 2>&1; then
    echo "uniffi-bindgen-go already available: $(command -v uniffi-bindgen-go)"
    exit 0
fi

cargo install uniffi-bindgen-go \
    --git "${UNIFFI_BINDGEN_GO_REPO}" \
    --tag "${UNIFFI_BINDGEN_GO_TAG}" \
    --locked

echo "Installed uniffi-bindgen-go ${UNIFFI_BINDGEN_GO_TAG}"
