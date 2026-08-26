# BENZHI_README

这是一个 Go 后端服务，用于一名导演独立完成 AI 漫剧生产与带教的后端服务。

## 项目说明

- 项目：vance1852/manjuflow-studio
- 项目用途：一名导演独立完成 AI 漫剧生产与带教的后端服务。系统把提示词资产、分镜生产编排与 教学工坊串成两条互相依赖的业务链：
- Go 工具链：`golang:1.22`
- 前端工具链：无

## 标准构建、运行和测试命令

进入容器后执行：

```bash
# 编译
cd '/app' && GOTOOLCHAIN=local go build ./...

# 启动
cd '/app' && GOTOOLCHAIN=local go run ./cmd/server

# 测试
cd '/app' && GOTOOLCHAIN=local go test ./...
```

## Docker 构建和进入容器

```bash
chmod +x build_benzhi_docker.sh
./build_benzhi_docker.sh benzhi-task-390-amd64 linux/amd64
./build_benzhi_docker.sh benzhi-task-390-arm64 linux/arm64
docker run -it benzhi-task-390-amd64:latest
docker run -it --platform linux/arm64 benzhi-task-390-arm64:latest
```

## 题目验证命令

1. 预期退出码 1：`go test ./internal/apptest -run '^TestRoutineSweepKeepsActiveSessionsSignedIn$' -count=1`
