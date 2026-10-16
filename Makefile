# Product source roots exclude generated binaries and historical probes.
GO_PACKAGES := ./apps/... ./adapters/... ./packages/... ./prompts/... ./tests/...

.PHONY: build check fmt internal-release npm-test ops-test rename-check test vet

build:
	go build ./apps/...

internal-release:
	apps/release/build-internal-release.sh

fmt:
	gofmt -w $$(find apps adapters packages prompts tests -type f -name '*.go')

test:
	go test $(GO_PACKAGES)
	go test -count=1 ./tests/integration

npm-test:
	npm ci --ignore-scripts --prefix installers/npm
	npm test --prefix installers/npm
	node --test apps/release/public-release.test.mjs
	node --test apps/dashboard/*.test.mjs

vet:
	go vet $(GO_PACKAGES)

rename-check:
	node tests/rename-allowlist.mjs

ops-test:
	PYTHONDONTWRITEBYTECODE=1 python3 -m unittest discover -s apps/release -p 'test_*.py'
	PYTHONDONTWRITEBYTECODE=1 python3 -m unittest discover -s deploy -p 'test_*.py'

check: rename-check npm-test ops-test test vet build
