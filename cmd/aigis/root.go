package main

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"

	"aigis/internal/config"
)

var cfgFile string

var rootCmd = &cobra.Command{
	Use:   "aigis",
	Short: "AI Security Gateway",
	Long:  `AI Security Gateway - A high-performance gateway for AI/LLM services.`,
}

func Execute() {
	if err := rootCmd.Execute(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func SetupRootCmd() {
	cobra.OnInitialize(func() {
		config.Init(cfgFile)
	})

	rootCmd.PersistentFlags().StringVar(&cfgFile, "config", "", "config file (default is ./configs/config.yaml)")
}
