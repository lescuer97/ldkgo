# cdkgo (dedicated Go bindings workspace)

This directory is structured as a standalone `cdkgo` repository that generates and verifies Go bindings for CDK using [NordSecurity/uniffi-bindgen-go](https://github.com/NordSecurity/uniffi-bindgen-go).

It does **not** depend on generated files living in `cashubtc/cdk`. Instead, CI/CD pulls CDK at a selected ref, builds `cdk-ffi`, and generates Go bindings here.

## What this does

- Clones/updates `cashubtc/cdk`
- Builds `crates/cdk-ffi` as a shared library
- Generates Go bindings with `uniffi-bindgen-go`
- Runs Go verification (`go test`) with CGO + native library linking
- Publishes artifacts in release workflow

## Directory layout

- `scripts/bootstrap-cdk.sh` – clone/update CDK checkout
- `scripts/install-uniffi-bindgen-go.sh` – install pinned generator version
- `scripts/generate-bindings.sh` – build `cdk-ffi` and generate bindings
- `scripts/verify-go.sh` – run Go verification with correct linker/runtime env
- `.github/workflows/` – CI and release workflows for this workspace

## Quick start

```bash
cd cdkgo
make generate
make verify
```

## Important environment variables

- `CDK_REF` (default: `main`) – CDK branch/tag/SHA to build
- `CDK_REPO` (default: `https://github.com/cashubtc/cdk.git`) – source repository
- `UNIFFI_BINDGEN_GO_TAG` (default: `v0.5.0+v0.29.5`) – generator version aligned with UniFFI 0.29
- `BUILD_PROFILE` (default: `release`) – Rust build profile used for `cdk-ffi` (`release`, `dev`, or custom profile)

## CI/CD model

- `ci.yml`: regenerates and verifies bindings on PR/push and fails if generated files were not committed.
- `update-bindings-pr.yml`: scheduled/manual workflow that regenerates bindings and opens a PR with updated generated files and prebuilt native libs (`linux_amd64`, `darwin_amd64`, `darwin_arm64`).
- `release.yml`: publishes release artifacts from committed bindings/native libs.

This gives you a PR-first flow for generated code updates, then release/tag publication.

## Using from other Go projects

```bash
go get github.com/asmogo/cdkgo
```

Prebuilt native libraries are committed in this repository under:

- `bindings/cdkffi/native/linux_amd64/libcdk_ffi.so`
- `bindings/cdkffi/native/darwin_amd64/libcdk_ffi.dylib`
- `bindings/cdkffi/native/darwin_arm64/libcdk_ffi.dylib`

The package includes platform-specific CGO link settings, so users do not need a separate manual unzip flow.

## Notes

- Go bindings generation follows the previous `go-ffi` approach (`--library` from built `libcdk_ffi`), but in a dedicated Go-focused workspace.
- If this directory is moved to its own repository, workflows continue to work with minimal/no changes.
