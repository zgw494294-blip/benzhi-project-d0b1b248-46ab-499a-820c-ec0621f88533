# BENZHI_README

## 项目说明
- 项目：benzhi-project-d0b1b248-46ab-499a-820c-ec0621f88533
- 项目用途：可运行的 Go 项目
- Go 工具链：`golang:1.27.0`
- 前端工具链：无

## 标准构建、运行和测试命令
cd '/app' && GOTOOLCHAIN=local go build ./...
cd '/app' && GOTOOLCHAIN=local go run ./cmd/arcproof
cd '/app' && GOTOOLCHAIN=local go test ./...

## Docker 构建和进入容器
chmod +x build_benzhi_docker.sh
./build_benzhi_docker.sh benzhi-project-d0b1b248-46ab-499a-820c-ec0621f88533-amd64 linux/amd64
./build_benzhi_docker.sh benzhi-project-d0b1b248-46ab-499a-820c-ec0621f88533-arm64 linux/arm64
