// Copyright (c) AIGis authors. All rights reserved.
// This file is part of the AIGis Enterprise Edition and is licensed under the
// AIGis Enterprise Edition License (see ee/LICENSE). It is NOT covered by the
// AGPLv3 that governs the open-source core. Commercial license required for use.

package main

import (
	"fmt"
	"os"
	"time"

	"github.com/spf13/cobra"
	"github.com/spf13/viper"

	eeadminui "aigis/ee/adminui"
	"aigis/ee/auth"
	"aigis/ee/billing"
	eequota "aigis/ee/quota"
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

		dsn := eeDSN()

		// --- Enterprise layer: inbound authentication / multi-tenant ---
		// Prefer the DB-backed key registry when a DSN is set (centrally managed,
		// revocable, keys stored as hashes) and expose /admin/keys to manage it.
		// Otherwise fall back to the static config map, or leave the gateway open
		// if neither is configured — like the OSS core.
		if dsn != "" {
			keyProvider, err := auth.NewPostgresAPIKeyProvider(cmd.Context(), dsn, globalLogger)
			if err != nil {
				return fmt.Errorf("failed to init auth store: %w", err)
			}
			defer keyProvider.Close()
			// Bootstrap: seed any config-declared keys into the DB so a fresh
			// deployment has a working key to call /admin/keys with. Keys listed
			// under ee.auth.admin_keys get admin privileges. Idempotent (upsert),
			// so it is safe on every restart.
			admins := adminKeys()
			for rawKey, tenant := range apiKeys() {
				if err := keyProvider.CreateKey(cmd.Context(), rawKey, tenant, tenant, admins[rawKey], auth.Actor{Subject: "bootstrap"}); err != nil {
					return fmt.Errorf("failed to seed API key: %w", err)
				}
			}
			// Keep this replica's snapshot in sync with keys created/revoked on
			// other replicas by periodically reloading from the DB.
			keyProvider.StartRefresh(authReloadInterval())

			// SaaS dashboard logins (human email+password) sit in front of the
			// API-key auth when a session store (Redis) is configured: the same
			// middleware accepts a session cookie OR a Bearer key, so programmatic
			// clients keep working. Without Redis we fall back to API-key only.
			if raddr, rpw, rdb := sessionRedis(); raddr != "" {
				userStore, err := auth.NewUserStore(cmd.Context(), dsn, globalLogger)
				if err != nil {
					return fmt.Errorf("failed to init user store: %w", err)
				}
				defer userStore.Close()
				sessionStore, err := auth.NewSessionStore(cmd.Context(), raddr, rpw, rdb, sessionTTL())
				if err != nil {
					return fmt.Errorf("failed to init session store: %w", err)
				}
				defer sessionStore.Close()
				// Bootstrap dashboard users from config (idempotent upsert).
				for _, u := range seedUsers() {
					email, _ := u["email"].(string)
					pw, _ := u["password"].(string)
					tenant, _ := u["tenant"].(string)
					admin, _ := u["admin"].(bool)
					if email == "" || pw == "" || tenant == "" {
						continue
					}
					if err := userStore.CreateUser(cmd.Context(), email, pw, tenant, admin); err != nil {
						return fmt.Errorf("failed to seed user %q: %w", email, err)
					}
				}
				srv.Use(auth.SessionMiddleware(userStore, sessionStore, keyProvider, globalLogger))
				globalLogger.Sugar().Info("EE auth: dashboard login enabled (session via Redis); /login /logout /me active")
			} else {
				srv.Use(auth.Middleware(keyProvider))
			}
			srv.Use(auth.AdminMiddleware(keyProvider, globalLogger))
			globalLogger.Sugar().Info("EE auth: API keys from DB; /admin/keys enabled")
		} else if keys := apiKeys(); len(keys) > 0 {
			srv.Use(auth.Middleware(auth.NewStaticAPIKeyProvider(keys)))
			globalLogger.Sugar().Infof("EE auth enabled: %d API key(s) loaded from config", len(keys))
		} else {
			globalLogger.Sugar().Warn("EE auth NOT configured (no DSN, ee.auth.api_keys empty) — gateway is open")
		}

		// --- Enterprise layer: usage metering / billing ---
		// Persist usage to PostgreSQL/TimescaleDB when a DSN is configured;
		// otherwise fall back to the in-memory sink (aggregate + log only).
		if dsn != "" {
			pgSink, err := billing.NewPostgresSinkWithOptions(cmd.Context(), dsn, globalLogger, billingOptions())
			if err != nil {
				return fmt.Errorf("failed to init billing store: %w", err)
			}
			defer pgSink.Close()
			srv.SetUsageSink(pgSink)
			// Read-only usage query API (GET /admin/usage). Registered after auth
			// so admin calls require a valid API key.
			srv.Use(billing.AdminMiddleware(pgSink, globalLogger))
			// Light up the keys/usage/audit tabs in the embedded admin dashboard;
			// the panels are backed by the /admin/* endpoints registered above.
			srv.Use(eeadminui.CapabilitiesMiddleware())
			globalLogger.Sugar().Info("EE billing: usage persisted to PostgreSQL; /admin/usage enabled")
		} else {
			srv.SetUsageSink(billing.NewMeteringSink(globalLogger))
			globalLogger.Sugar().Warn("EE billing: ee.billing.dsn not set — usage kept in memory only")
		}

		// --- Enterprise layer: per-tenant quota / rate limiting ---
		// Enforce a per-tenant in-flight ceiling when configured (0 = unlimited).
		// With ee.quota.redis_addr set, the ceiling is shared across all replicas
		// (distributed); otherwise it is per-process (in-memory).
		perTenant, defLimit := quotaConfig()
		if defLimit > 0 || len(perTenant) > 0 {
			if addr := viper.GetString("ee.quota.redis_addr"); addr != "" {
				rl, err := eequota.NewRedisLimiter(cmd.Context(), addr,
					viper.GetString("ee.quota.redis_password"), viper.GetInt("ee.quota.redis_db"),
					perTenant, defLimit, globalLogger)
				if err != nil {
					return fmt.Errorf("failed to init distributed quota: %w", err)
				}
				defer rl.Close()
				srv.SetQuotaLimiter(rl)
				globalLogger.Sugar().Infof("EE quota enabled (distributed via Redis): default=%d, per-tenant overrides=%d", defLimit, len(perTenant))
			} else {
				srv.SetQuotaLimiter(eequota.NewConcurrencyLimiter(perTenant, defLimit))
				globalLogger.Sugar().Infof("EE quota enabled (in-memory, single-replica): default=%d, per-tenant overrides=%d", defLimit, len(perTenant))
			}
		}

		return srv.Start()
	},
}

// apiKeys reads the "ee.auth.api_keys" config section: a map of apiKey -> tenant.
func apiKeys() map[string]string {
	return viper.GetStringMapString("ee.auth.api_keys")
}

// adminKeys reads "ee.auth.admin_keys" (a list of raw keys that should be
// granted admin privileges) into a set for O(1) lookup during bootstrap.
func adminKeys() map[string]bool {
	set := make(map[string]bool)
	for _, k := range viper.GetStringSlice("ee.auth.admin_keys") {
		set[k] = true
	}
	return set
}

// authReloadInterval returns how often the DB-backed key provider should reload
// its snapshot to pick up keys created/revoked on other replicas. Reads
// ee.auth.reload_interval_sec; defaults to 30s; <=0 disables (single-replica).
func authReloadInterval() time.Duration {
	if !viper.IsSet("ee.auth.reload_interval_sec") {
		return 30 * time.Second
	}
	return time.Duration(viper.GetInt("ee.auth.reload_interval_sec")) * time.Second
}

// eeDSN resolves the shared Enterprise datastore DSN (auth registry + usage
// store), preferring the AIGIS_EE_BILLING_DSN environment variable over the
// ee.billing.dsn config key so the DB password never has to be committed to
// config.yaml.
func eeDSN() string {
	if dsn := os.Getenv("AIGIS_EE_BILLING_DSN"); dsn != "" {
		return dsn
	}
	return viper.GetString("ee.billing.dsn")
}

// sessionRedis resolves where dashboard login sessions are stored. It prefers
// ee.auth.session.redis_addr, falling back to the quota Redis (ee.quota.redis_addr)
// so a single Redis can serve both. Empty means no session store -> the SaaS
// login is disabled and the gateway stays API-key only.
func sessionRedis() (addr, password string, db int) {
	addr = viper.GetString("ee.auth.session.redis_addr")
	if addr != "" {
		return addr, viper.GetString("ee.auth.session.redis_password"), viper.GetInt("ee.auth.session.redis_db")
	}
	return viper.GetString("ee.quota.redis_addr"),
		viper.GetString("ee.quota.redis_password"),
		viper.GetInt("ee.quota.redis_db")
}

// sessionTTL reads ee.auth.session.ttl_hours; 0/unset falls back to the store
// default (24h).
func sessionTTL() time.Duration {
	return time.Duration(viper.GetInt("ee.auth.session.ttl_hours")) * time.Hour
}

// seedUsers reads the ee.auth.users bootstrap list: each entry declares a
// dashboard login (email/password/tenant/admin) to upsert on startup.
func seedUsers() []map[string]any {
	var out []map[string]any
	raw, ok := viper.Get("ee.auth.users").([]any)
	if !ok {
		return out
	}
	for _, item := range raw {
		if m, ok := item.(map[string]any); ok {
			out = append(out, m)
		}
	}
	return out
}

// billingOptions reads the async usage-writer tuning from config. All keys are
// optional; unset fields fall back to the billing package defaults (zero
// behaviour change).
//
//	ee.billing.queue_size          -> buffered event channel capacity
//	ee.billing.batch_size          -> events per DB round-trip
//	ee.billing.flush_interval_ms   -> periodic flush tick (milliseconds)
//	ee.billing.max_retries         -> batch write retries on DB error
func billingOptions() billing.SinkOptions {
	return billing.SinkOptions{
		QueueSize:     viper.GetInt("ee.billing.queue_size"),
		BatchSize:     viper.GetInt("ee.billing.batch_size"),
		FlushInterval: time.Duration(viper.GetInt("ee.billing.flush_interval_ms")) * time.Millisecond,
		MaxRetries:    viper.GetInt("ee.billing.max_retries"),
	}
}

// quotaConfig reads per-tenant concurrency limits from config:
//
//	ee.quota.default            -> fallback max in-flight per tenant (0 = unlimited)
//	ee.quota.per_tenant.<name>  -> explicit override for one tenant
//
// It returns the per-tenant override map and the default ceiling.
func quotaConfig() (perTenant map[string]int, def int) {
	def = viper.GetInt("ee.quota.default")
	perTenant = make(map[string]int)
	for tenant, v := range viper.GetStringMap("ee.quota.per_tenant") {
		perTenant[tenant] = cast(v)
	}
	return perTenant, def
}

// cast coerces a viper config value to an int, tolerating int/float/string.
func cast(v interface{}) int {
	switch n := v.(type) {
	case int:
		return n
	case int64:
		return int(n)
	case float64:
		return int(n)
	default:
		return 0
	}
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
