[default]
_list:
    @just --list

# Compile sources and build the executable
build:
    @echo "Building executable:"
    go build
    @echo "Build complete"

# Run test cases
test: build
    @echo "Running test cases:"
    go test -v ./...
    @echo "Completed running test cases"

# Runs clean build and tests, including race
clean-build-and-test:
    @echo "Building executable (cache disabled):"
    go build -a
    @echo "Build complete"
    @echo "Now running tests:"
    go test -race -v ./...
    @echo "Testing complete"

alias cbt := clean-build-and-test

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
