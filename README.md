# ldkgo

Go FFI bindings for an LDK Node native library, packaged for Linux amd64.

The generated Go bindings and the prebuilt `libldk_node.so` binary are committed to this repository, so downstream consumers only need Go with CGO enabled.

## Usage

```bash
go get github.com/lescuer97/ldkgo
```

Import the package from `github.com/lescuer97/ldkgo/bindings/ldk_node_ffi`.

## Supported platform

| OS    | Arch  | Library           |
|-------|-------|-------------------|
| Linux | amd64 | `libldk_node.so`  |

## Development

### Prerequisites

- Go 1.22+
- `CGO_ENABLED=1`
- Linux amd64 host

Rust and `uniffi-bindgen-go` are not required unless you decide to regenerate the bindings outside this repository's checked-in artifacts.

### Validating bindings

```bash
make generate   # validate checked-in Linux binding artifacts and refresh checksums
make verify     # CGO_ENABLED=1 go test ./bindings/ldk_node_ffi
make clean      # remove generated linker stub
```

## CI/CD

| Workflow                 | Trigger            | Description                                         |
|--------------------------|--------------------|-----------------------------------------------------|
| `ci.yml`                 | Push / PR          | Validates Linux binding artifacts and Go package    |
| `update-bindings-pr.yml` | Manual / scheduled | Refreshes checksum metadata and opens an update PR  |
| `release.yml`            | `v*` tag           | Publishes a Linux-only release archive              |
