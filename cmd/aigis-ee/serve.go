// Copyright (c) AIGis authors. All rights reserved.
// This file is part of the AIGis Enterprise Edition and is licensed under the
// AIGis Enterprise Edition License (see ee/LICENSE). It is NOT covered by the
// AGPLv3 that governs the open-source core. Commercial license required for use.

package main

import (
	"fmt"
	"os"
	"strconv"
	"time"

	"github.com/spf13/cobra"
	"github.com/spf13/viper"

	eeadminui "aigis/ee/adminui"
	"aigis/ee/auth"
	"aigis/ee/billing"
	eequota "aigis/ee/quota"
	"aigis/internal/config"
	corequota "aigis/internal/core/quota"
	"aigis/internal/core/usage"
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

			// Optional near-real-time invalidation: with ee.auth.pubsub on and a
			// Redis available, broadcast key changes so peers refresh within a
			// second instead of waiting for the poll tick. Polling stays on as the
			// fail-safe. Reuses the session/quota Redis endpoint.
			if pubsubEnabled() {
				if raddr, rpw, rdb := sessionRedis(); raddr != "" {
					kcRedis, err := auth.NewKeyChangeRedis(cmd.Context(), raddr, rpw, rdb)
					if err != nil {
						return fmt.Errorf("failed to init key-change pub/sub: %w", err)
					}
					keyProvider.SetPublisher(kcRedis)
					keyProvider.StartSubscribe(cmd.Context(), kcRedis)
					globalLogger.Sugar().Info("EE auth: key-change pub/sub enabled (sub-second cross-replica invalidation)")
				} else {
					globalLogger.Sugar().Warn("EE auth: ee.auth.pubsub set but no Redis configured — falling back to polling only")
				}
			}

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
				reg := allowRegister()
				// SMTP-backed account flows (email verify / password reset / role
				// management). Installed IN FRONT OF the session middleware so it can
				// intercept /register when verification is on. When SMTP is not
				// configured the mailer is a no-op and these routes are not mounted.
				mailer, err := auth.NewMailer(mailerConfig())
				if err != nil {
					return fmt.Errorf("failed to init mailer: %w", err)
				}
				emailOpts := auth.EmailOptions{
					Verify:         emailVerify(),
					BaseURL:        dashboardBaseURL(),
					PlatformTenant: platformTenant(),
				}
				if mailer.Enabled() {
					// Public email flows run BEFORE auth (token-verified, not
					// session-authenticated).
					srv.Use(auth.EmailMiddleware(userStore, mailer, emailOpts, globalLogger))
				}
				srv.Use(auth.SessionMiddleware(userStore, sessionStore, keyProvider, reg, globalLogger))
				if mailer.Enabled() {
					// Role management runs AFTER auth so the admin Principal is set.
					srv.Use(auth.RoleMiddleware(userStore, emailOpts, globalLogger))
					globalLogger.Sugar().Infof("EE auth: SMTP mailer enabled; /verify /forgot /reset active + /admin/users/role (email_verify=%v)", emailVerify())
				}
				if reg {
					globalLogger.Sugar().Info("EE auth: dashboard login enabled (session via Redis); /login /logout /me /register active")
				} else {
					globalLogger.Sugar().Info("EE auth: dashboard login enabled (session via Redis); /login /logout /me active (self-register off)")
				}
			} else {
				srv.Use(auth.Middleware(keyProvider))
			}
			srv.Use(auth.AdminMiddleware(keyProvider, platformTenant(), globalLogger))
			globalLogger.Sugar().Info("EE auth: API keys from DB; /admin/keys enabled")
		} else if keys := apiKeys(); len(keys) > 0 {
			srv.Use(auth.Middleware(auth.NewStaticAPIKeyProvider(keys)))
			globalLogger.Sugar().Infof("EE auth enabled: %d API key(s) loaded from config", len(keys))
		} else {
			globalLogger.Sugar().Warn("EE auth NOT configured (no DSN, ee.auth.api_keys empty) — gateway is open")
		}

		// --- Enterprise layer: usage metering / billing ---
		// Persist usage to PostgreSQL/TimescaleDB when a DSN is configured;
		// otherwise fall back to the in-memory sink (aggregate + log only). The
		// chosen base sink is installed below, after the quota block, so the token
		// gate can wrap it (TokenMeteringSink) when token quota is enabled.
		var usageSink usage.Sink
		if dsn != "" {
			pgSink, err := billing.NewPostgresSinkWithOptions(cmd.Context(), dsn, globalLogger, billingOptions())
			if err != nil {
				return fmt.Errorf("failed to init billing store: %w", err)
			}
			defer pgSink.Close()
			usageSink = pgSink
			// Read-only usage query API (GET /admin/usage). Registered after auth
			// so admin calls require a valid API key.
			srv.Use(billing.AdminMiddleware(pgSink, platformTenant(), globalLogger))
			// Light up the keys/usage/audit tabs in the embedded admin dashboard;
			// the panels are backed by the /admin/* endpoints registered above.
			srv.Use(eeadminui.CapabilitiesMiddleware())
			globalLogger.Sugar().Info("EE billing: usage persisted to PostgreSQL; /admin/usage enabled")
		} else {
			usageSink = billing.NewMeteringSink(globalLogger)
			globalLogger.Sugar().Warn("EE billing: ee.billing.dsn not set — usage kept in memory only")
		}

		// --- Enterprise layer: per-tenant quota / rate limiting ---
		// Three independent per-tenant dimensions, any subset may be enabled:
		//   - concurrency: max in-flight requests (ee.quota.default / per_tenant)
		//   - QPS: max requests per one-second window (ee.quota.qps_default /
		//     qps_per_tenant)
		//   - tokens: max tokens per period (ee.quota.token_default /
		//     token_per_tenant / token_period)
		// With ee.quota.redis_addr set each enabled gate is shared across all
		// replicas (distributed); otherwise it is per-process (in-memory). The
		// gates are composed into a single TenantLimiter injected via the seam.
		ccPerTenant, ccDef := quotaConfig()
		qpsPerTenant, qpsDef := qpsConfig()
		tokPerTenant, tokDef, tokPeriod := tokenConfig()
		ccOn := ccDef > 0 || len(ccPerTenant) > 0
		qpsOn := qpsDef > 0 || len(qpsPerTenant) > 0
		tokOn := tokDef > 0 || len(tokPerTenant) > 0

		var tokenGate eequota.TokenGate
		if ccOn || qpsOn || tokOn {
			redisAddr := viper.GetString("ee.quota.redis_addr")
			distributed := redisAddr != ""

			var concurrency corequota.Limiter
			if ccOn {
				if distributed {
					rl, err := eequota.NewRedisLimiter(cmd.Context(), redisAddr,
						viper.GetString("ee.quota.redis_password"), viper.GetInt("ee.quota.redis_db"),
						ccPerTenant, ccDef, globalLogger)
					if err != nil {
						return fmt.Errorf("failed to init distributed concurrency quota: %w", err)
					}
					defer rl.Close()
					concurrency = rl
				} else {
					concurrency = eequota.NewConcurrencyLimiter(ccPerTenant, ccDef)
				}
			}

			var rate eequota.RateGate
			if qpsOn {
				if distributed {
					rl, err := eequota.NewRedisRateLimiter(cmd.Context(), redisAddr,
						viper.GetString("ee.quota.redis_password"), viper.GetInt("ee.quota.redis_db"),
						qpsPerTenant, qpsDef, globalLogger)
					if err != nil {
						return fmt.Errorf("failed to init distributed QPS quota: %w", err)
					}
					defer rl.Close()
					rate = rl
				} else {
					rate = eequota.NewRateLimiter(qpsPerTenant, qpsDef)
				}
			}

			if tokOn {
				if distributed {
					tl, err := eequota.NewRedisTokenLimiter(cmd.Context(), redisAddr,
						viper.GetString("ee.quota.redis_password"), viper.GetInt("ee.quota.redis_db"),
						tokPerTenant, tokDef, tokPeriod, globalLogger)
					if err != nil {
						return fmt.Errorf("failed to init distributed token quota: %w", err)
					}
					defer tl.Close()
					tokenGate = tl
				} else {
					tokenGate = eequota.NewTokenLimiter(tokPerTenant, tokDef, tokPeriod)
				}
			}

			srv.SetQuotaLimiter(eequota.NewTenantLimiter(rate, tokenGate, concurrency))
			mode := "in-memory, single-replica"
			if distributed {
				mode = "distributed via Redis"
			}
			globalLogger.Sugar().Infof("EE quota enabled (%s): concurrency(default=%d,overrides=%d) qps(default=%d,overrides=%d) tokens(default=%d,overrides=%d,period=%s)",
				mode, ccDef, len(ccPerTenant), qpsDef, len(qpsPerTenant), tokDef, len(tokPerTenant), viper.GetString("ee.quota.token_period"))
		}

		// Install the usage sink last: when token quota is on, wrap the billing
		// sink so every completed request books its tokens against the budget the
		// admission gate reads (write half of the token quota; core stays unchanged).
		if tokenGate != nil {
			usageSink = eequota.NewTokenMeteringSink(usageSink, tokenGate)
		}
		srv.SetUsageSink(usageSink)

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

// platformTenant returns the tenant whose admins are platform-wide (cross-tenant
// visibility on /admin/*). Any other tenant's admins are confined to their own
// tenant. Reads ee.auth.platform_tenant; defaults to "ops".
func platformTenant() string {
	if v := viper.GetString("ee.auth.platform_tenant"); v != "" {
		return v
	}
	return "ops"
}

// allowRegister reports whether the self-service signup route (POST /register)
// is exposed. Reads ee.auth.allow_register; defaults to false so a deployment
// must opt in — an open registration endpoint invites spam accounts.
func allowRegister() bool {
	return viper.GetBool("ee.auth.allow_register")
}

// pubsubEnabled reports whether key changes should be broadcast over Redis
// pub/sub for sub-second cross-replica invalidation. Reads ee.auth.pubsub;
// defaults to false (polling-only). Requires a Redis endpoint to take effect.
func pubsubEnabled() bool {
	return viper.GetBool("ee.auth.pubsub")
}

// mailerConfig assembles SMTP settings for transactional email (verify/reset).
// Secrets come from environment variables (never committed): AIGIS_SMTP_HOST,
// AIGIS_SMTP_PORT, AIGIS_SMTP_USER, AIGIS_SMTP_PASSWORD, AIGIS_SMTP_FROM. An
// empty host yields a no-op mailer (email-gated flows stay off).
func mailerConfig() auth.SMTPConfig {
	port := 0
	if v := os.Getenv("AIGIS_SMTP_PORT"); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			port = n
		}
	}
	return auth.SMTPConfig{
		Host:     os.Getenv("AIGIS_SMTP_HOST"),
		Port:     port,
		User:     os.Getenv("AIGIS_SMTP_USER"),
		Password: os.Getenv("AIGIS_SMTP_PASSWORD"),
		From:     os.Getenv("AIGIS_SMTP_FROM"),
	}
}

// emailVerify reports whether self-service signup requires email verification
// before the account can log in. Reads ee.auth.email_verify; defaults to false
// (immediate activation, preserving C1 behavior). Only takes effect when a
// Mailer is configured.
func emailVerify() bool {
	return viper.GetBool("ee.auth.email_verify")
}

// dashboardBaseURL is the externally reachable origin used to build links in
// emails (e.g. https://app.example.com). Reads ee.auth.dashboard_url; empty
// yields relative links.
func dashboardBaseURL() string {
	return viper.GetString("ee.auth.dashboard_url")
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
//	ee.billing.wal.dir             -> WAL directory (empty = WAL disabled, default)
//	ee.billing.wal.max_seg_mb      -> rotate the active WAL segment past this size
//	ee.billing.wal.replay_interval_sec -> how often to sweep the WAL back to the DB
func billingOptions() billing.SinkOptions {
	return billing.SinkOptions{
		QueueSize:         viper.GetInt("ee.billing.queue_size"),
		BatchSize:         viper.GetInt("ee.billing.batch_size"),
		FlushInterval:     time.Duration(viper.GetInt("ee.billing.flush_interval_ms")) * time.Millisecond,
		MaxRetries:        viper.GetInt("ee.billing.max_retries"),
		WALDir:            viper.GetString("ee.billing.wal.dir"),
		WALMaxSegBytes:    int64(viper.GetInt("ee.billing.wal.max_seg_mb")) * 1024 * 1024,
		WALReplayInterval: time.Duration(viper.GetInt("ee.billing.wal.replay_interval_sec")) * time.Second,
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

// qpsConfig reads per-tenant QPS (requests-per-second) limits from config,
// mirroring quotaConfig for the rate dimension:
//
//	ee.quota.qps_default            -> fallback max requests/sec per tenant (0 = unlimited)
//	ee.quota.qps_per_tenant.<name>  -> explicit override for one tenant
//
// It returns the per-tenant override map and the default ceiling.
func qpsConfig() (perTenant map[string]int, def int) {
	def = viper.GetInt("ee.quota.qps_default")
	perTenant = make(map[string]int)
	for tenant, v := range viper.GetStringMap("ee.quota.qps_per_tenant") {
		perTenant[tenant] = cast(v)
	}
	return perTenant, def
}

// tokenConfig reads per-tenant token-budget limits from config, mirroring
// quotaConfig for the cumulative-token dimension:
//
//	ee.quota.token_default            -> fallback max tokens per period per tenant (0 = unlimited)
//	ee.quota.token_per_tenant.<name>  -> explicit override for one tenant
//	ee.quota.token_period             -> reset granularity: day (default) | hour | month
//
// It returns the per-tenant override map, the default ceiling, and the period.
func tokenConfig() (perTenant map[string]int, def int, period eequota.Period) {
	def = viper.GetInt("ee.quota.token_default")
	perTenant = make(map[string]int)
	for tenant, v := range viper.GetStringMap("ee.quota.token_per_tenant") {
		perTenant[tenant] = cast(v)
	}
	return perTenant, def, eequota.ParsePeriod(viper.GetString("ee.quota.token_period"))
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
