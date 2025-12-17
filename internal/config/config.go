package config

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/joho/godotenv"
	"github.com/spf13/viper"

	"aigis/internal/core/engine"
)

// findEnvFile 向上递归查找 .env 文件
func findEnvFile() string {
	dir, err := os.Getwd()
	if err != nil {
		return ""
	}

	for {
		envFile := filepath.Join(dir, ".env")
		if _, err := os.Stat(envFile); err == nil {
			return envFile
		}

		parent := filepath.Dir(dir)
		if parent == dir {
			// 已到达根目录
			break
		}
		dir = parent
	}
	return ""
}

// Init 初始化配置，加载 .env 和 config.yaml
func Init(cfgFile string) {
	// Try to load .env file from current directory and search upwards
	if err := godotenv.Load(); err != nil {
		if envFile := findEnvFile(); envFile != "" {
			if err := godotenv.Load(envFile); err == nil {
				fmt.Fprintf(os.Stderr, "Loaded .env file from: %s\n", envFile)
			} else {
				fmt.Fprintf(os.Stderr, "Warning: error loading .env file from %s: %v\n", envFile, err)
			}
		} else {
			fmt.Fprintf(os.Stderr, "Warning: .env file not found in current or parent directories\n")
		}
	} else {
		fmt.Fprintf(os.Stderr, "Loaded .env file from current directory\n")
	}

	if cfgFile != "" {
		viper.SetConfigFile(cfgFile)
	} else {
		viper.SetConfigName("config")
		viper.SetConfigType("yaml")
		viper.AddConfigPath("./configs")
		viper.AddConfigPath(".")
	}

	// Environment variables
	viper.SetEnvPrefix("AIGIS")
	viper.SetEnvKeyReplacer(strings.NewReplacer(".", "_"))
	viper.AutomaticEnv()

	// Read config file
	if err := viper.ReadInConfig(); err != nil {
		if _, ok := err.(viper.ConfigFileNotFoundError); !ok {
			fmt.Fprintf(os.Stderr, "Error reading config file: %v\n", err)
		}
	}
}

// LoadEngineConfig loads and returns the engine configuration from viper
func LoadEngineConfig() (*engine.EngineConfig, error) {
	var config engine.EngineConfig

	// Unmarshal the engine section
	if err := viper.UnmarshalKey("engine", &config); err != nil {
		return nil, fmt.Errorf("failed to unmarshal engine config: %w", err)
	}

	return &config, nil
}
