package main

import (
	"flag"
	"fmt"
	"log"

	"qq-bot/cmd/server/internal/config"
)

func main() {
	configPath := flag.String("config", "", "配置文件路径（默认自动从项目根目录查找 config.yaml）")
	flag.Parse()

	cfg, err := config.Load(*configPath)
	if err != nil {
		log.Fatalf("加载配置失败：%v", err)
	}

	fmt.Printf("QQ AppID: %s\n", cfg.QQ.AppID)
	fmt.Printf("LLM BaseURL: %s\n", cfg.LLM.BaseURL)
	fmt.Printf("LLM Model: %s\n", cfg.LLM.Model)
}
