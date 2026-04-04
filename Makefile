.PHONY: build test coverage fmt clean test-docker-build test-docker-ensure test-safe test-safe-race

APP_NAME=haystack
BUILD_DIR=build

ifeq ($(OS),Windows_NT)
	MKDIR_P = powershell -NoProfile -Command "if (-not (Test-Path '$(BUILD_DIR)')) { New-Item -ItemType Directory -Path '$(BUILD_DIR)' | Out-Null }"
	RM_RF = powershell -NoProfile -Command "if (Test-Path '$(BUILD_DIR)') { Remove-Item -Recurse -Force '$(BUILD_DIR)' }"
	EXE_EXT = .exe
	COVERAGE_CMD = scripts\coverage.cmd detail
else
	MKDIR_P = mkdir -p $(BUILD_DIR)
	RM_RF = rm -rf $(BUILD_DIR)
	EXE_EXT =
	COVERAGE_CMD = ./scripts/coverage.sh detail
endif

APP_BIN=$(BUILD_DIR)/$(APP_NAME)$(EXE_EXT)

build:
	@$(MKDIR_P)
	@echo "Building $(APP_NAME)..."
	@go build -o $(APP_BIN) ./cmd/haystack/
	@echo "Build complete: $(APP_BIN)"

test:
	@echo "Running tests..."
	@go test ./... -count=1

coverage:
	@echo "Running tests with coverage..."
	@$(COVERAGE_CMD)

fmt:
	@echo "Formatting Go code..."
	@find . -name "*.go" -not -path "./vendor/*" | xargs gofmt -w
	@echo "Formatting complete."

clean:
	@echo "Cleaning..."
	@$(RM_RF)

DOCKER_TEST_IMAGE=haystack-test

test-docker-build:
	@docker build -f Dockerfile.test -t $(DOCKER_TEST_IMAGE) .

test-docker-ensure:
	@if [ -z "$$(docker images -q $(DOCKER_TEST_IMAGE) 2>/dev/null)" ]; then \
		echo "Building test image (first time or after cleanup)..."; \
		$(MAKE) test-docker-build; \
	fi

# NOTE: --network=none blocks all network access. If integration tests need
# localhost networking, use 'go test' directly or adjust this flag.
test-safe: test-docker-ensure
	@echo "Running tests in Docker (isolated)..."
	@docker run --rm --cpus=2 --memory=2g --pids-limit=256 --network=none \
		-v $$(pwd):/app:ro \
		$(DOCKER_TEST_IMAGE)

test-safe-race: test-docker-ensure
	@echo "Running tests with race detector in Docker (isolated)..."
	@docker run --rm --cpus=2 --memory=4g --pids-limit=256 --network=none \
		-v $$(pwd):/app:ro \
		$(DOCKER_TEST_IMAGE) bash -c "ulimit -u 256 && go test -race ./... -count=1 -timeout 5m"
