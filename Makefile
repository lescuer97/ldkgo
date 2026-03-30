SHELL := /usr/bin/env bash

.PHONY: bootstrap generate verify verify-checksums update-checksums clean

bootstrap:
	./scripts/bootstrap-ldk-node.sh

generate:
	./scripts/generate-bindings.sh

verify:
	./scripts/verify-go.sh

verify-checksums:
	./scripts/verify-checksums.sh

update-checksums:
	./scripts/update-checksums.sh

clean:
	rm -f bindings/ldk_node_ffi/link_linux_amd64.go
