.PHONY: test test-race vet build release release-test check fmt-check

test:
	go test ./...

test-race:
	go test -race ./...

vet:
	go vet ./...

build:
	go build -o ./bin/wtree ./cmd/wtree

# release creates only local artifacts; publishing is intentionally out of scope.
release:
	VERSION=$${VERSION:?set VERSION, for example VERSION=1.2.3} DIST_DIR=$${DIST_DIR:-dist} ./scripts/release-build.sh

release-test:
	./scripts/release-build_test.sh

fmt-check:
	@unformatted="$$(go list -f '{{.Dir}}' ./... | while IFS= read -r dir; do gofmt -l "$$dir"/*.go; done)"; \
	test -z "$$unformatted" || (printf '%s\n' "$$unformatted" && exit 1)

check: fmt-check vet test test-race build
