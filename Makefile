.PHONY: all tidy fmt vet test race build run run-oracle demo tutorial clean

BIN := bin

all: fmt vet test build

tidy:
	go mod tidy

fmt:
	gofmt -w .

vet:
	go vet ./...

test:
	go test ./...

race:
	go test -race ./...

build:
	go build -o $(BIN)/api ./cmd/api
	go build -o $(BIN)/ruleloader ./cmd/ruleloader

run: ## 文件规则源启动（默认配置）
	go run ./cmd/api -f configs/config.toml

run-oracle: ## Oracle 规则源启动
	go run ./cmd/api -f configs/config.oracle.toml

demo: ## 服务启动后另开终端执行：跑通评估/查询/热更新
	./scripts/demo.sh

tutorial: ## 运行 grule 入门教学示例
	go run ./examples/tutorial

clean:
	rm -rf $(BIN)
