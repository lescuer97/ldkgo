#!/usr/bin/env bash
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
ROOT_DIR="$(cd "${SCRIPT_DIR}/.." && pwd)"
WORK_DIR="${ROOT_DIR}/.work"
CDK_DIR="${WORK_DIR}/cdk"

CDK_REPO="${CDK_REPO:-https://github.com/asmogo/cdk.git}"
CDK_REF="${CDK_REF:-main}"

mkdir -p "${WORK_DIR}"

if [ ! -d "${CDK_DIR}/.git" ]; then
    git clone --depth 1 "${CDK_REPO}" "${CDK_DIR}"
fi

git -C "${CDK_DIR}" fetch --tags --prune origin
git -C "${CDK_DIR}" checkout "${CDK_REF}"
git -C "${CDK_DIR}" pull --ff-only origin "${CDK_REF}" || true

echo "CDK checkout ready at ${CDK_DIR} ($(git -C "${CDK_DIR}" rev-parse --short HEAD))"
