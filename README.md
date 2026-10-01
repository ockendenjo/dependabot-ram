# dependabot-ram (rebase-and-merge)

Utility script for getting dependabot PRs merged:

## Functionality

For each dependabot PR:

* Rebase on the default branch
* Wait for the PR checks to run
* Merge the PR

## tasks

### build

Compiles the binary to `build/depram`.

```
GOOS=linux GOARCH=amd64 CGO_ENABLED=0 go build -o build/depram .
```

### install

Requires: build

Copies the binary to `GOBIN`.

```
cp build/depram $(go env GOBIN)/depram
```

### sast

```shell
go fix -diff ./...
wget -O .golangci.json https://raw.githubusercontent.com/ockendenjo/actions/refs/heads/main/.golangci.json
golangci-lint run
govulncheck ./...
```
