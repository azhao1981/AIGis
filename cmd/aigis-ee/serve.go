// Copyright (c) AIGis authors. All rights reserved.
// This file is part of the AIGis Enterprise Edition and is licensed under the
// AIGis Enterprise Edition License (see ee/LICENSE). It is NOT covered by the
// AGPLv3 that governs the open-source core. Commercial license required for use.

package main

import (
	"fmt"

	"github.com/spf13/cobra"
	"github.com/spf13/viper"

	"aigis/ee/auth"
	"aigis/internal/config"
	"aigis/internal/pkg/logger"
	"aigis/internal/server"
)

var version = "dev"

var cfgFile string

var rootCmd = &cobra.Command{
	Use:   "aigis-ee",
	Short: "AI Security Gateway (Enterprise Edition)",
	Long:  `AIGis Enterprise Edition — the open-source gateway plus inbound auth / multi-tenant.`,
}

var versionCmd = &cobra.Command{
	Use:   "version",
	Short: "Print the version number",
	Run: func(cmd *cobra.Command, args []string) {
		fmt.Println("aigis-ee", version)
	},
}

var serveCmd = &cobra.Command{
	Use:   "serve",
	Short: "Start the AIGis Enterprise server",
	RunE: func(cmd *cobra.Command, args []string) error {
		logLevel := viper.GetString("log.level")
		if logLevel == "" {
			logLevel = "info"
		}

		rot := logger.DefaultRotation()
		if viper.IsSet("log.rotate") {
			rot.Enabled = viper.GetBool("log.rotate")
		}
		if v := viper.GetString("log.file"); v != "" {
			rot.Filename = v
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

		// --- Enterprise layer: inbound authentication / multi-tenant ---
		// Register the auth middleware only when API keys are configured, so an
		// unconfigured EE binary still boots (open by default, like the OSS core).
		if keys := apiKeys(); len(keys) > 0 {
			srv.Use(auth.Middleware(auth.NewStaticAPIKeyProvider(keys)))
			globalLogger.Sugar().Infof("EE auth enabled: %d API key(s) loaded", len(keys))
		} else {
			globalLogger.Sugar().Warn("EE auth NOT configured (ee.auth.api_keys empty) — gateway is open")
		}

		return srv.Start()
	},
}

// apiKeys reads the "ee.auth.api_keys" config section: a map of apiKey -> tenant.
func apiKeys() map[string]string {
	return viper.GetStringMapString("ee.auth.api_keys")
}

func init() {
	cobra.OnInitialize(func() { config.Init(cfgFile) })
	rootCmd.PersistentFlags().StringVar(&cfgFile, "config", "", "config file (default is ./configs/config.yaml)")

	serveCmd.Flags().IntP("port", "p", 8080, "Server port")
	serveCmd.Flags().StringP("host", "H", "0.0.0.0", "Server host")
	viper.BindPFlag("server.port", serveCmd.Flags().Lookup("port"))
	viper.BindPFlag("server.host", serveCmd.Flags().Lookup("host"))

	rootCmd.AddCommand(serveCmd)
	rootCmd.AddCommand(versionCmd)
}
