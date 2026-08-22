.PHONY: test test-race vet build release release-test tutorial-test check fmt-check

TEST_TIMEOUT ?= 15m

test:
	go test -timeout=$(TEST_TIMEOUT) ./...

test-race:
	go test -race -timeout=$(TEST_TIMEOUT) ./...

vet:
	go vet ./...

build:
	go build -o ./bin/wtree ./cmd/wtree

install:
	go install ./cmd/wtree

# release creates only local artifacts; publishing is intentionally out of scope.
release:
	VERSION=$${VERSION:?set VERSION, for example VERSION=1.2.3} DIST_DIR=$${DIST_DIR:-dist} ./scripts/release-build.sh

release-test:
	./scripts/release-build_test.sh

tutorial-test:
	./tutorial/run-all-commands.sh

fmt-check:
	@unformatted="$$(go list -f '{{.Dir}}' ./... | while IFS= read -r dir; do gofmt -l "$$dir"/*.go; done)"; \
	test -z "$$unformatted" || (printf '%s\n' "$$unformatted" && exit 1)

check: fmt-check vet test test-race build tutorial-test
