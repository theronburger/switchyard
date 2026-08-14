.PHONY: app-bundle check check-format format go-check swift-check test race

GO_FILES := $(shell find cmd internal -name '*.go' -type f 2>/dev/null)

check: check-format go-check swift-check

check-format:
	@test -z "$$(gofmt -l $(GO_FILES))" || { gofmt -l $(GO_FILES); exit 1; }

format:
	gofmt -w $(GO_FILES)

go-check:
	go vet ./...
	go test ./...

swift-check:
	swift build --package-path app
	swift run --package-path app SwitchyardContractCheck contracts/v1/fixtures/status.json

test:
	go test ./...
	swift run --package-path app SwitchyardContractCheck contracts/v1/fixtures/status.json

race:
	go test -race ./...

app-bundle:
	./scripts/build-app-bundle.sh
