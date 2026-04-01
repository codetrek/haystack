.PHONY: build test coverage fmt clean

APP_NAME=haystack
BUILD_DIR=build
SRC_DIR=src

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
	@cd $(SRC_DIR) && go build -o ../$(APP_BIN) ./
	@echo "Build complete: $(APP_BIN)"

test:
	@echo "Running tests..."
	@cd $(SRC_DIR) && go test ./... -count=1

coverage:
	@echo "Running tests with coverage..."
	@$(COVERAGE_CMD)

fmt:
	@echo "Formatting Go code..."
	@cd $(SRC_DIR) && find . -name "*.go" -not -path "./vendor/*" | xargs gofmt -w
	@echo "Formatting complete."

clean:
	@echo "Cleaning..."
	@$(RM_RF)
