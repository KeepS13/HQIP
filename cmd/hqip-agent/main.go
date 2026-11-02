package main

import (
	"context"
	"flag"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"github.com/KeepS13/HQIP/internal/agent"
)

func main() {
	configPath := flag.String("config", "/etc/hqip-node/agent.json", "Agent JSON 配置文件")
	flag.Parse()
	logger := slog.New(slog.NewJSONHandler(os.Stderr, nil))
	cfg, err := agent.LoadConfig(*configPath)
	if err != nil {
		logger.Error("读取 Agent 配置失败", "error", err)
		os.Exit(1)
	}
	a, err := agent.New(cfg, logger)
	if err != nil {
		logger.Error("初始化 Agent 失败", "error", err)
		os.Exit(1)
	}
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	if err := a.Run(ctx); err != nil {
		logger.Error("Agent 已停止", "error", err)
		_ = a.Close()
		os.Exit(1)
	}
	if err := a.Close(); err != nil {
		logger.Error("关闭 Agent 数据库失败", "error", err)
		os.Exit(1)
	}
}
