# AIGis Makefile

# 变量定义
BINARY_NAME=aigis
BUILD_DIR=bin
MAIN_PATH=cmd/aigis/main.go
CONFIG_PATH=configs/config.yaml

# Go 相关变量
GOTOOLCHAIN=local
GO=GOTOOLCHAIN=$(GOTOOLCHAIN) go
GOFMT=gofmt
GOLINT=golint
GOVET=GOTOOLCHAIN=$(GOTOOLCHAIN) go vet
GOTEST=GOTOOLCHAIN=$(GOTOOLCHAIN) go test

# 检查 Go 版本
GO_VERSION := $(shell $(GO) version | grep -o 'go[0-9]\+\.[0-9]\+' | sed 's/go//')

# 构建标志
LDFLAGS=-ldflags "-s -w"
BUILD_FLAGS=-v $(LDFLAGS)

# 检查 Go 版本
.PHONY: check-go
check-go:
	@echo "检查 Go 环境..."
	@$(GO) version
	@if [ "$$($(GO) version | grep -o 'go[0-9]\+\.[0-9]\+')" != "go1.23" ] && \
	[ "$$($(GO) version | grep -o 'go[0-9]\+\.[0-9]\+')" != "go1.24" ] && \
	[ "$$($(GO) version | grep -o 'go[0-9]\+\.[0-9]\+')" != "go1.25" ]; then \
		echo "警告: 推荐使用 Go 1.23、1.24 或 1.25"; \
	fi

# 默认目标
.PHONY: all
all: check-go clean build

# 构建
.PHONY: build
build:
	@echo "构建 $(BINARY_NAME)..."
	@mkdir -p $(BUILD_DIR)
	$(GO) build $(BUILD_FLAGS) -o $(BUILD_DIR)/$(BINARY_NAME) ./cmd/aigis
	@echo "构建完成: $(BUILD_DIR)/$(BINARY_NAME)"

# 运行服务
.PHONY: run
run: build
	@echo "启动服务..."
	./$(BUILD_DIR)/$(BINARY_NAME) serve

# 运行服务（指定端口）
.PHONY: run-port
run-port: build
	@echo "启动服务在端口 $(PORT)..."
	./$(BUILD_DIR)/$(BINARY_NAME) serve -p $(PORT)

# 开发模式（热重载）
.PHONY: dev
dev:
	@echo "开发模式启动..."
	@if [ "$(GO_VERSION)" = "1.23" ] || [ "$(GO_VERSION)" = "1.24" ]; then \
		echo "air 需要 Go 1.25 或更高版本，当前版本为 $(GO_VERSION)"; \
		echo "请手动运行: go run ./cmd/aigis serve"; \
	else \
		command -v air >/dev/null 2>&1 || { echo "请先安装 air: go install github.com/cosmtrek/air@latest"; exit 1; }; \
		air; \
	fi

# 测试
.PHONY: test
test:
	@echo "运行测试..."
	@if [ -d "test" ] || find . -name "*_test.go" -type f -exec test -e {} \; 2>/dev/null; then \
		$(GOTEST) -v ./...; \
	else \
		echo "没有找到测试文件，跳过测试"; \
	fi

# 测试覆盖率
.PHONY: test-coverage
test-coverage:
	@echo "运行测试并生成覆盖率报告..."
	@if [ -d "test" ] || find . -name "*_test.go" -type f -exec test -e {} \; 2>/dev/null; then \
		$(GOTEST) -v -coverprofile=coverage.out ./... && \
		$(GO) tool cover -html=coverage.out -o coverage.html && \
		echo "覆盖率报告已生成: coverage.html"; \
	else \
		echo "没有找到测试文件，跳过测试覆盖率生成"; \
	fi

# 代码格式化
.PHONY: fmt
fmt:
	@echo "格式化代码..."
	$(GOFMT) -s -w .

# 代码检查
.PHONY: lint
lint:
	@echo "运行代码检查..."
	$(GOVET) ./...
	@echo "提示: golint 检查已跳过，如需使用请运行: go install golang.org/x/lint/golint@latest"

# 清理
.PHONY: clean
clean:
	@echo "清理构建产物..."
	@rm -rf $(BUILD_DIR)
	@rm -f coverage.out coverage.html
	@echo "清理完成"

# 安装依赖
.PHONY: deps
deps:
	@echo "安装依赖..."
	$(GO) mod download
	$(GO) mod tidy

# 更新依赖
.PHONY: update-deps
update-deps:
	@echo "更新依赖..."
	$(GO) get -u ./...
	$(GO) mod tidy

# 生成 mock 文件（如果使用 mockery）
.PHONY: mock
mock:
	@echo "生成 mock 文件..."
	@command -v mockery >/dev/null 2>&1 || { echo "请先安装 mockery: go install github.com/vektra/mockery/v2@latest"; exit 1; }
	mockery --all

# 安装开发工具
.PHONY: install-tools
install-tools:
	@echo "安装开发工具..."
	@if [ "$(GO_VERSION)" = "1.23" ] || [ "$(GO_VERSION)" = "1.24" ]; then \
		echo "注意: air 需要 Go 1.25+，当前版本 $(GO_VERSION)"; \
		echo "跳过 air 安装"; \
	else \
		echo "安装 air..."; \
		$(GO) install github.com/cosmtrek/air@latest; \
	fi
	@echo "安装其他工具..."
	$(GO) install github.com/golangci/golangci-lint/cmd/golangci-lint@latest
	$(GO) install github.com/vektra/mockery/v2@latest

# Docker 相关
.PHONY: docker-build
docker-build:
	@echo "构建 Docker 镜像..."
	@command -v docker >/dev/null 2>&1 || { echo "Docker 未安装，请先安装 Docker"; exit 1; }
	docker build -t $(BINARY_NAME):latest .

.PHONY: docker-run
docker-run:
	@echo "运行 Docker 容器..."
	@command -v docker >/dev/null 2>&1 || { echo "Docker 未安装，请先安装 Docker"; exit 1; }
	docker run -p 8080:8080 $(BINARY_NAME):latest

# 帮助信息
.PHONY: help
help:
	@echo "可用的 make 目标:"
	@echo "  all          - 清理并构建"
	@echo "  build        - 构建二进制文件"
	@echo "  run          - 构建并运行服务（默认端口）"
	@echo "  run-port     - 构建并运行服务（使用 PORT 变量指定端口）"
	@echo "  dev          - 开发模式运行（需要 air）"
	@echo "  test         - 运行测试"
	@echo "  test-coverage- 运行测试并生成覆盖率报告"
	@echo "  fmt          - 格式化代码"
	@echo "  lint         - 代码检查"
	@echo "  clean        - 清理构建产物"
	@echo "  deps         - 安装依赖"
	@echo "  update-deps  - 更新依赖"
	@echo "  mock         - 生成 mock 文件"
	@echo "  install-tools- 安装开发工具"
	@echo "  docker-build - 构建 Docker 镜像"
	@echo "  docker-run   - 运行 Docker 容器"
	@echo "  help         - 显示此帮助信息"
	@echo ""
	@echo "示例:"
	@echo "  make run PORT=3000    # 在端口 3000 运行服务"
	@echo "  make dev              # 开发模式"
	@echo "  make test-coverage    # 生成测试覆盖率报告"