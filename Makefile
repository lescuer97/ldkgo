SHELL := /usr/bin/env bash

.PHONY: bootstrap install-bindgen generate verify clean

bootstrap:
	./scripts/bootstrap-cdk.sh

install-bindgen:
	./scripts/install-uniffi-bindgen-go.sh

generate:
	./scripts/generate-bindings.sh

verify:
	./scripts/verify-go.sh

clean:
	rm -rf .work bindings/cdkffi
