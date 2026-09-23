.PHONY: test check build run verify

test:
	GOWORK=off GOPROXY=off go test -race ./...
	cd host && GOWORK=off GOPROXY=off go test -race ./...
	npm --prefix web test

check:
	GOWORK=off GOPROXY=off go vet ./...
	cd host && GOWORK=off GOPROXY=off go vet ./...
	npm --prefix web run typecheck

build:
	npm --prefix web ci
	npm --prefix web test
	node build.mjs
	node check-ui.mjs

run:
	dist/host

verify:
	python3 verify.py
