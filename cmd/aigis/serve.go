package main

import (
	"fmt"

	"github.com/spf13/cobra"
	"github.com/spf13/viper"

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

		globalLogger, err := logger.New(logLevel)
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
		srv := server.NewHTTPServer(addr, globalLogger)
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
