package main

import (
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/spf13/cobra"
	"github.com/spf13/viper"
	"go.uber.org/zap"

	"aigis/internal/pkg/logger"
	"aigis/internal/server"
)

var serveCmd = &cobra.Command{
	Use:   "serve",
	Short: "Start the AIGis server",
	Long:  `Start the AIGis HTTP server and begin accepting requests.`,
	RunE: func(cmd *cobra.Command, args []string) error {
		// 初始化全局 logger
		logLevel := viper.GetString("log.level")
		if logLevel == "" {
			logLevel = "info"
		}

		// 日志滚动配置（缺省走 logger.DefaultRotation：rotate 关闭 = 单文件，交给 logrotate）
		// 仅当显式 log.rotate=true 时才启用内置 lumberjack 滚动。
		rot := logger.DefaultRotation()
		if viper.IsSet("log.rotate") {
			rot.Enabled = viper.GetBool("log.rotate")
		}
		if v := viper.GetString("log.file"); v != "" {
			rot.Filename = v
		}
		if viper.IsSet("log.max_size_mb") {
			rot.MaxSizeMB = viper.GetInt("log.max_size_mb")
		}
		if viper.IsSet("log.max_backups") {
			rot.MaxBackups = viper.GetInt("log.max_backups")
		}
		if viper.IsSet("log.max_age_days") {
			rot.MaxAgeDays = viper.GetInt("log.max_age_days")
		}
		if viper.IsSet("log.compress") {
			rot.Compress = viper.GetBool("log.compress")
		}

		globalLogger, err := logger.NewWithRotation(logLevel, rot)
		if err != nil {
			return fmt.Errorf("failed to initialize logger: %w", err)
		}
		defer globalLogger.Sync()

		port := viper.GetInt("server.port")
		if port == 0 {
			port = 8080
		}

		host := viper.GetString("server.host")
		if host == "" {
			host = "0.0.0.0"
		}

		addr := fmt.Sprintf("%s:%d", host, port)
		srv, err := server.NewHTTPServer(addr, globalLogger)
		if err != nil {
			return fmt.Errorf("failed to create server: %w", err)
		}

		// Hot reload on SIGHUP: re-reads engine.routes + security.custom_rules
		// from viper and atomically swaps them in (fail loud: bad config leaves
		// the running state untouched). Other config knobs still need restart.
		sigCh := make(chan os.Signal, 1)
		signal.Notify(sigCh, syscall.SIGHUP)
		go func() {
			for range sigCh {
				if err := srv.ReloadConfig(); err != nil {
					globalLogger.Error("SIGHUP reload failed (live config unchanged)",
						zap.Error(err),
					)
				}
			}
		}()

		return srv.Start()
	},
}

func SetupServeCmd() {
	rootCmd.AddCommand(serveCmd)

	serveCmd.Flags().IntP("port", "p", 8080, "Server port")
	serveCmd.Flags().StringP("host", "H", "0.0.0.0", "Server host")

	viper.BindPFlag("server.port", serveCmd.Flags().Lookup("port"))
	viper.BindPFlag("server.host", serveCmd.Flags().Lookup("host"))
}
