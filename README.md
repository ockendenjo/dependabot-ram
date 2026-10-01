# dependabot-ram (rebase-and-merge)

Utility script for getting dependabot PRs merged:

## Functionality

For each dependabot PR:

* Rebase on the default branch
* Wait for the PR checks to run
* Merge the PR

## tasks

### build

Compiles the binary to `build/aprc`.

```
GOOS=linux GOARCH=amd64 CGO_ENABLED=0 go build -o build/aprc .
```

### install

Requires: build

Copies the binary to `GOBIN`.

```
cp build/aprc $(go env GOBIN)/aprc
```

### sast

```shell
wget -O .golangci.json https://raw.githubusercontent.com/ockendenjo/actions/refs/heads/main/.golangci.json
golangci-lint run
govulncheck ./...
```
