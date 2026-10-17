MODULE  := github.com/styd/magenta
VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
COMMIT  ?= $(shell git rev-parse --short HEAD 2>/dev/null || echo unknown)
DATE    ?= $(shell date -u +%Y-%m-%dT%H:%M:%SZ)
LDFLAGS := -s -w \
	-X $(MODULE)/internal/cli.Version=$(VERSION) \
	-X $(MODULE)/internal/cli.Commit=$(COMMIT) \
	-X $(MODULE)/internal/cli.Date=$(DATE)

export CGO_ENABLED := 0

.PHONY: build test bench install vet fmt completions smoke

build:
	mkdir -p bin
	go build -ldflags '$(LDFLAGS)' -o bin/ ./cmd/...

test:
	go vet ./...
	go test ./...

# End to end check of the pager and search with the real fzf, through tmux.
smoke:
	sh test/smoke.sh

bench:
	go test -run '^$$' -bench . -benchmem ./...

install:
	go install -ldflags '$(LDFLAGS)' ./cmd/...

vet:
	go vet ./...

fmt:
	gofmt -l -w .

# Shell completions for magenta, as packaged by the release.
completions: build
	mkdir -p completions
	for sh in bash zsh fish; do bin/magenta completion $$sh > completions/magenta.$$sh; done
