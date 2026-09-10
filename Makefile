.PHONY: app-bundle check check-format ci format go-check icons release-checks release-dry-run swift-check test race

GO_FILES := $(shell rg --files cmd internal -g '*.go')

check: check-format go-check swift-check

check-format:
	@test -z "$$(gofmt -l $(GO_FILES))" || { gofmt -l $(GO_FILES); exit 1; }

format:
	gofmt -w $(GO_FILES)

go-check:
	go vet ./...
	go test ./...

swift-check:
	./scripts/check-swift.sh

test: go-check swift-check

race:
	go test -race ./...

app-bundle:
	./scripts/build-app-bundle.sh

ci:
	./scripts/ci.sh

release-checks:
	./scripts/release-checks.sh

release-dry-run:
	./scripts/build-release.sh dist

icons:
	./scripts/generate-icons.sh
