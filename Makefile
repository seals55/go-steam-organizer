BINARY_NAME := steam-organizer
ifeq ($(OS),Windows_NT)
	BINARY_EXT := .exe
else
	BINARY_EXT :=
endif
BINARY := $(BINARY_NAME)$(BINARY_EXT)
COVERAGE_OUT := coverage.out
COVERAGE_HTML := coverage.html

.PHONY: all clean build test coverage vet fmt tidy help

## all: Run clean, test, and build in sequence (default target)
all: clean test build

## build: Compile the application binary
build:
	@echo "==> Building $(BINARY)..."
	go build -v -o $(BINARY) .

## test: Run all unit and integration tests with coverage summary
test:
	@echo "==> Running tests..."
	go test -v -cover ./...

## coverage: Generate coverage profile and HTML report
coverage:
	@echo "==> Generating coverage report..."
	go test -coverprofile=$(COVERAGE_OUT) ./...
	go tool cover -html=$(COVERAGE_OUT) -o $(COVERAGE_HTML)
	@echo "==> Coverage report written to $(COVERAGE_HTML)"

## vet: Run go vet static analysis
vet:
	@echo "==> Running go vet..."
	go vet ./...

## fmt: Format all Go source files
fmt:
	@echo "==> Formatting Go files..."
	go fmt ./...

## tidy: Tidy go.mod and go.sum
tidy:
	@echo "==> Tidying Go module dependencies..."
	go mod tidy

## clean: Remove build artifacts, test cache, and coverage outputs
clean:
	@echo "==> Cleaning build and test artifacts..."
	go clean -testcache
ifeq ($(OS),Windows_NT)
	-@powershell -NoProfile -Command "Remove-Item -Force -ErrorAction SilentlyContinue '$(BINARY)', '$(BINARY_NAME).exe', '$(BINARY_NAME)', '$(COVERAGE_OUT)', '$(COVERAGE_HTML)'"
else
	rm -f $(BINARY) $(BINARY_NAME).exe $(BINARY_NAME) $(COVERAGE_OUT) $(COVERAGE_HTML)
endif

## help: Show available Makefile targets
help:
	@echo "Available targets:"
	@echo "  make all       - Clean, run all tests, and build $(BINARY) (default)"
	@echo "  make clean     - Remove built binaries, coverage files, and test cache"
	@echo "  make test      - Run unit and integration tests with coverage"
	@echo "  make coverage  - Generate $(COVERAGE_OUT) and $(COVERAGE_HTML)"
	@echo "  make build     - Build $(BINARY)"
	@echo "  make vet       - Run go vet static checks"
	@echo "  make fmt       - Format Go source code"
	@echo "  make tidy      - Run go mod tidy"

