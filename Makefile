# `make` is optional: every target here is one plain `go` command, and the
# README gives the raw equivalents. Nothing in the install path needs make,
# or Go at all: `bin/cache-ttl-tracker` (the dispatcher) downloads the
# matching binary from this repo's GitHub Releases on first run.

# Stripping debug info (-s -w) takes ~3.6MB down to ~2.4MB per platform.
PLATFORMS := linux/amd64 linux/arm64 windows/amd64 windows/arm64 darwin/amd64 darwin/arm64

.PHONY: build release test vet fmt-check check clean

# The host platform only, named so the bin/cache-ttl-tracker dispatcher
# finds it locally without a download. Never build to bin/cache-ttl-tracker
# itself: that path is the committed dispatcher script, and overwriting it
# breaks every other platform.
build:
	go build -ldflags="-s -w" \
		-o bin/cache-ttl-tracker-$$(go env GOOS)-$$(go env GOARCH)$$(test "$$(go env GOOS)" = windows && echo .exe) \
		./cmd/cache-ttl-tracker

# Every shipped platform, cross-compiled from whichever machine runs this.
# CI runs this on a version tag and attaches the results to a GitHub
# Release via `gh release create` (.github/workflows/release.yml) -- these
# binaries are never committed to the repo, only uploaded as release assets.
release:
	@for target in $(PLATFORMS); do \
		os=$${target%/*}; arch=$${target#*/}; ext=""; \
		[ "$$os" = windows ] && ext=".exe"; \
		echo "building $$os/$$arch"; \
		GOOS=$$os GOARCH=$$arch go build -ldflags="-s -w" \
			-o "bin/cache-ttl-tracker-$$os-$$arch$$ext" ./cmd/cache-ttl-tracker || exit 1; \
	done

test:
	go test ./...

vet:
	go vet ./...

fmt-check:
	@test -z "$$(gofmt -l .)" || (echo "gofmt needs to be run on:" && gofmt -l . && exit 1)

check: vet fmt-check test

# Deliberately does NOT remove bin/ wholesale: the dispatcher script lives
# there and is tracked. Everything this removes is gitignored (built or
# downloaded, never committed).
clean:
	rm -f bin/cache-ttl-tracker-*
