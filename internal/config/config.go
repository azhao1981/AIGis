package config

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/joho/godotenv"
	"github.com/spf13/viper"

	"aigis/internal/core/breaker"
	"aigis/internal/core/engine"
	"aigis/internal/core/security"
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

// AuditEnabled reports whether sensitive-info audit logging is on.
// Defaults to true when the `audit.enabled` key is absent from config.
func AuditEnabled() bool {
	if !viper.IsSet("audit.enabled") {
		return true
	}
	return viper.GetBool("audit.enabled")
}

// BreakerConfig returns the per-route circuit-breaker config from the `breaker`
// section. Disabled by default; fail_threshold/cooldown_sec fall back to sane
// defaults (5 / 30s) when absent or non-positive.
func BreakerConfig() breaker.Config {
	ft := viper.GetInt("breaker.fail_threshold")
	if ft <= 0 {
		ft = 5
	}
	cd := viper.GetInt("breaker.cooldown_sec")
	if cd <= 0 {
		cd = 30
	}
	return breaker.Config{
		Enabled:       viper.GetBool("breaker.enabled"),
		FailThreshold: ft,
		Cooldown:      time.Duration(cd) * time.Second,
	}
}

// MaxConcurrent returns the configured global in-flight request ceiling from
// `limit.max_concurrent`. Defaults to 0 (unlimited) when the key is absent.
func MaxConcurrent() int {
	return viper.GetInt("limit.max_concurrent")
}

// LoadCustomRules loads user-defined sensitive-info detection rules from the
// `security.custom_rules` config section. Returns nil when the key is absent.
func LoadCustomRules() ([]security.CustomRule, error) {
	var rules []security.CustomRule
	if err := viper.UnmarshalKey("security.custom_rules", &rules); err != nil {
		return nil, fmt.Errorf("failed to unmarshal security.custom_rules: %w", err)
	}
	return rules, nil
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
