// Package main 提供 RustSwitch 控制进程入口、配置校验和两阶段退出信号处理。
package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"rustswitch/control/internal/config"
	"rustswitch/control/internal/esl"
	"rustswitch/control/internal/server"
	"strings"
	"syscall"
)

// main 先校验启动配置，再创建媒体与管理服务；首次退出信号排空通话，第二次信号强制取消。
func main() {
	path := flag.String("config", "config/local.json", "configuration file")
	check := flag.Bool("check-config", false, "validate configuration without binding sockets")
	// ESL 默认不开放；密钥从私有文件读取，避免出现在进程参数或日志中。
	eslListen := flag.String("esl-listen", "", "optional inbound ESL loopback address, e.g. 127.0.0.1:8021")
	eslPasswordFile := flag.String("esl-password-file", "", "private file containing the ESL password")
	flag.Parse()
	var eslPassword string
	if *eslListen != "" || *eslPasswordFile != "" {
		if *eslListen == "" || *eslPasswordFile == "" {
			slog.Error("ESL requires both listen address and password file")
			os.Exit(1)
		}
		info, e := os.Stat(*eslPasswordFile)
		if e != nil || !info.Mode().IsRegular() || info.Size() > 257 || info.Mode().Perm()&0077 != 0 {
			slog.Error("ESL password file must be a private regular file of at most 257 bytes")
			os.Exit(1)
		}
		secret, e := os.ReadFile(*eslPasswordFile)
		if e != nil {
			slog.Error("cannot read ESL password file")
			os.Exit(1)
		}
		eslPassword = strings.TrimSuffix(strings.TrimSuffix(string(secret), "\n"), "\r")
		if eslPassword == "" || len(eslPassword) > 256 || strings.ContainsAny(eslPassword, "\r\n\x00") {
			slog.Error("invalid ESL password file content")
			os.Exit(1)
		}
	}
	c, err := config.Load(*path)
	if err != nil {
		slog.Error("configuration error", "error", err)
		os.Exit(1)
	}
	if *check {
		// 管理页面保存的待启动配置会覆盖对应启动参数，校验模式必须一并检查而不绑定端口。
		if err := server.CheckConfiguration(c); err != nil {
			slog.Error("configuration error", "error", err)
			os.Exit(1)
		}
		fmt.Println("configuration valid")
		return
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	app, err := server.New(ctx, c)
	if err != nil {
		slog.Error("startup failed", "error", err)
		os.Exit(1)
	}
	defer app.Close()
	if *eslListen != "" {
		events, e := esl.Listen(ctx, esl.Options{Listen: *eslListen, Password: eslPassword, API: app.CompatibilityAPI, Execute: app.CompatibilityExecute})
		if e != nil {
			slog.Error("ESL startup failed", "error", e)
			app.Close()
			os.Exit(1)
		}
		app.SetCompatibilityEvents(events.Publish)
		defer events.Close()
		slog.Info("inbound ESL ready", "listen", events.Addr().String())
	}
	signals := make(chan os.Signal, 2)
	signal.Notify(signals, syscall.SIGINT, syscall.SIGTERM)
	defer signal.Stop(signals)
	stop := make(chan struct{})
	// 信号只转换为排空/取消通知，不在信号协程中直接访问呼叫映射或释放媒体。
	go func() {
		select {
		case <-signals:
			close(stop)
		case <-ctx.Done():
			return
		}
		select {
		case <-signals:
			cancel()
		case <-ctx.Done():
		}
	}()
	// 日志使用管理草稿应用后的真实配置，避免重启切换端口后仍报告原始文件参数。
	slog.Info("RustSwitch ready", "sip", app.Config.SIP.Listen, "admin", app.Config.Admin.Listen, "media_workers", app.Config.Media.Workers, "max_calls", app.Config.Limits.MaxCalls)
	if err := app.Run(stop); err != nil && err != context.Canceled {
		slog.Error("controller stopped", "error", err)
	}
}
