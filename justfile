[default]
_list:
    @just --list

# Compile sources and build the executable for local platform
build:
    @echo "Building executable:"
    go build
    @echo "Build complete"

# Run test cases
test: build
    @echo "Running test cases:"
    go test -v ./...
    @echo "Completed running test cases"

# Runs clean build, lint, tests (including race) etc.
ci:
    @echo "Checking formatting"
    @test -z $(gofmt -l .) || (echo "Some files need formatting. Run 'go fmt ./...'" && exit 1)
    @echo "Formatting check complete"
    @echo "Library check"
    go mod tidy
    go mod verify
    git diff --exit-code go.mod go.sum
    @echo "Library check completed"
    @echo "Building executable (cache disabled) for multiple platforms:"
    GOOS=linux GOARCH=amd64 go build -a
    GOOS=freebsd GOARCH=amd64 go build -a
    GOOS=windows GOARCH=amd64 go build -a
    GOOS=android GOARCH=arm64 go build -a
    GOOS=darwin GOARCH=arm64 go build -a
    @echo "Build complete"
    @echo "Run lint:"
    go vet ./...
    @echo "Lint complete"
    @echo "Now running tests:"
    go test -count=1 -race -v ./...
    @echo "Testing complete"

# Run tests and capture coverage
test-coverage:
    go test -count=1 -race -coverprofile=coverage.out -covermode=atomic ./...
    go tool cover -func=coverage.out

# Run build, tests and install
install: test
    @echo "Installing rsync-sidekick:"
    go install .
    @echo "Check if install is successful:"
    rsync-sidekick --version

# Build the docker image
docker-build:
    docker build -t manumk/rsync-sidekick:latest .

alias db := docker-build

# Update Git repo (stashes changes, if any)
update:
    git stash -u
    git checkout main
    git pull --rebase
    @echo "You may want to run \`just bat\`"
