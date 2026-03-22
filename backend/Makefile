# NovaStream Backend Makefile

.PHONY: build run test clean deps fmt lint recover-account

# Build the application
build:
	go build -o bin/mediastorm main.go

# Run the application
run:
	go run main.go

# Run tests
test:
	go test -v ./...

# Clean build artifacts
clean:
	go clean
	rm -rf bin/

# Install dependencies
deps:
	go mod download
	go mod tidy

# Format code
fmt:
	go fmt ./...

# Lint code
lint:
	golint ./...

# Run all checks
check: fmt lint test

# Reset an account password from the backend without logging in.
# Example:
# make recover-account ARGS='-master -generate'
# make recover-account ARGS='-username alice -new-password s3cret'
recover-account:
	go run ./tools/accountrecovery $(ARGS)

# Development server with hot reload (requires air)
dev:
	air

# Build for multiple platforms
build-all:
	GOOS=linux GOARCH=amd64 go build -o bin/mediastorm-linux-amd64 ./main.go
	GOOS=windows GOARCH=amd64 go build -o bin/mediastorm-windows-amd64.exe ./main.go
	GOOS=darwin GOARCH=amd64 go build -o bin/mediastorm-darwin-amd64 ./main.go
	GOOS=darwin GOARCH=arm64 go build -o bin/mediastorm-darwin-arm64 ./main.go

# Docker build
docker-build:
	docker build -t mediastorm .

# Docker run
docker-run:
	docker run -p 7777:7777 mediastorm

# Install development tools
install-tools:
	go install github.com/cosmtrek/air@latest
	go install golang.org/x/lint/golint@latest
	go install github.com/go-delve/delve/cmd/dlv@latest
