package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/google/uuid"
	"github.com/ivp/ivp/internal/api"
	"github.com/ivp/ivp/internal/artifact"
	"github.com/ivp/ivp/internal/persistence"
	"github.com/ivp/ivp/internal/security"
	"github.com/ivp/ivp/internal/service"
	"github.com/ivp/ivp/internal/telemetry"
	"github.com/jackc/pgx/v5"
	"go.opentelemetry.io/otel"
)

func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo}))
	slog.SetDefault(logger)
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if len(os.Args) > 1 && os.Args[1] == "migrate" {
		if err := migrate(ctx); err != nil {
			logger.Error("database migration failed", "error", security.Redact(err.Error(), 300))
			os.Exit(1)
		}
		logger.Info("database migrations applied")
		return
	}
	if len(os.Args) > 1 && os.Args[1] == "maintenance" {
		if len(os.Args) != 3 || os.Args[2] != "retention" {
			logger.Error("invalid maintenance command", "usage", "ivp maintenance retention")
			os.Exit(2)
		}
		if err := runRetention(ctx, logger); err != nil {
			logger.Error("retention maintenance failed", "error", security.Redact(err.Error(), 300))
			os.Exit(1)
		}
		return
	}
	if err := serve(ctx, logger); err != nil {
		logger.Error("API server stopped", "error", security.Redact(err.Error(), 300))
		os.Exit(1)
	}
}

func migrate(ctx context.Context) error {
	db, err := persistence.Open(ctx, os.Getenv("IVP_DATABASE_URL"))
	if err != nil {
		return err
	}
	defer db.Close()
	return db.Migrate(ctx)
}

func serve(ctx context.Context, logger *slog.Logger) error {
	dsn := os.Getenv("IVP_DATABASE_URL")
	if dsn == "" {
		return errors.New("IVP_DATABASE_URL is required")
	}
	db, err := persistence.Open(ctx, dsn)
	if err != nil {
		return err
	}
	defer db.Close()
	if developmentAuthEnabled() {
		if err := db.Migrate(ctx); err != nil {
			return fmt.Errorf("apply development database migrations: %w", err)
		}
		if err := ensureDevelopmentIdentity(ctx, db, os.Getenv("IVP_DEV_TENANT_ID"), os.Getenv("IVP_DEV_USER_ID"), os.Getenv("IVP_DEV_SUBJECT")); err != nil {
			return fmt.Errorf("initialize development identity: %w", err)
		}
	}
	metrics := telemetry.NewMetrics()
	tracing, err := telemetry.NewTracing("ivp-api", nil)
	if err != nil {
		return err
	}
	defer func() {
		shutdown, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if e := tracing.Shutdown(shutdown); e != nil {
			logger.Warn("trace shutdown failed", "error", e)
		}
	}()
	otel.SetTracerProvider(tracing.Provider)
	otel.SetTextMapPropagator(telemetry.Propagator())
	var auth func(http.Handler) http.Handler
	if developmentAuthEnabled() {
		tenant, user, subject := os.Getenv("IVP_DEV_TENANT_ID"), os.Getenv("IVP_DEV_USER_ID"), os.Getenv("IVP_DEV_SUBJECT")
		if tenant == "" || user == "" || subject == "" {
			return errors.New("development auth requires IVP_DEV_TENANT_ID, IVP_DEV_USER_ID and IVP_DEV_SUBJECT")
		}
		logger.Warn("development-only local authentication is enabled")
		auth = func(next http.Handler) http.Handler {
			return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Header.Get("X-Tenant-ID") != tenant {
					http.Error(w, "tenant selection denied", http.StatusForbidden)
					return
				}
				next.ServeHTTP(w, r.WithContext(security.WithPrincipal(r.Context(), security.Principal{Subject: subject, TenantID: tenant, UserID: user, Role: security.RoleAdmin})))
			})
		}
	} else {
		issuer, audience := os.Getenv("IVP_OIDC_ISSUER"), os.Getenv("IVP_OIDC_AUDIENCE")
		if issuer == "" || audience == "" {
			return errors.New("IVP_OIDC_ISSUER and IVP_OIDC_AUDIENCE are required outside development mode")
		}
		provider, err := security.NewOIDCAuthenticator(ctx, issuer, audience, db)
		if err != nil {
			return fmt.Errorf("initialize OIDC: %w", err)
		}
		auth = provider.Middleware
	}
	backend := service.NewBackend(db, metrics)
	artifactStore, err := configuredArtifactStore()
	if err != nil {
		return fmt.Errorf("initialize artifact storage: %w", err)
	}
	artifactManager := &service.ArtifactManager{DB: db, Store: artifactStore, MaxBytes: artifact.DefaultMaxBytes, Retention: service.DefaultArtifactRetention, Metrics: metrics}
	handler := api.NewRouter(api.Options{Backend: backend, Artifacts: artifactManager, Authenticator: auth, Metrics: metrics, Tracing: tracing.Tracer, Idempotency: db, MaxBodyBytes: 24 << 20, MaxArtifactBytes: artifact.DefaultMaxBytes, RateLimitPerSecond: 25, RateLimitBurst: 50})
	addr := os.Getenv("IVP_LISTEN_ADDR")
	if addr == "" {
		addr = ":8080"
	}
	server := &http.Server{Addr: addr, Handler: handler, ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 30 * time.Second, WriteTimeout: 60 * time.Second, IdleTimeout: 90 * time.Second, MaxHeaderBytes: 1 << 20}
	serveErr := make(chan error, 1)
	go func() {
		cert, key := os.Getenv("IVP_TLS_CERT_FILE"), os.Getenv("IVP_TLS_KEY_FILE")
		if cert != "" || key != "" {
			if cert == "" || key == "" {
				serveErr <- errors.New("both IVP_TLS_CERT_FILE and IVP_TLS_KEY_FILE must be set")
				return
			}
			serveErr <- server.ListenAndServeTLS(cert, key)
			return
		}
		serveErr <- server.ListenAndServe()
	}()
	logger.Info("IVP API listening", "address", addr, "tls", os.Getenv("IVP_TLS_CERT_FILE") != "")
	select {
	case <-ctx.Done():
		shutdown, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		return server.Shutdown(shutdown)
	case err := <-serveErr:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return err
	}
}

func runRetention(ctx context.Context, logger *slog.Logger) error {
	days, err := retentionDays(os.Getenv("IVP_RAW_HISTORY_RETENTION_DAYS"))
	if err != nil {
		return err
	}
	db, err := persistence.Open(ctx, os.Getenv("IVP_DATABASE_URL"))
	if err != nil {
		return err
	}
	defer db.Close()
	artifactStore, err := configuredArtifactStore()
	if err != nil {
		return fmt.Errorf("initialize artifact storage: %w", err)
	}
	now := time.Now().UTC()
	result, err := db.PruneExpiredWithArtifacts(ctx, now, now.Add(-time.Duration(days)*24*time.Hour), func(ctx context.Context, key string) error {
		return artifactStore.Delete(ctx, key)
	})
	if err != nil {
		return err
	}
	logger.Info("retention maintenance completed", "expired_artifacts_deleted", result.ArtifactsDeleted, "raw_history_rows_deleted", result.HistoryRowsDeleted, "raw_history_retention_days", days)
	return nil
}

func configuredArtifactStore() (artifact.Store, error) {
	switch strings.ToLower(strings.TrimSpace(os.Getenv("IVP_ARTIFACT_BACKEND"))) {
	case "", "filesystem", "fs":
		root := strings.TrimSpace(os.Getenv("IVP_ARTIFACT_DIR"))
		if root == "" {
			cache, err := os.UserCacheDir()
			if err != nil {
				return nil, err
			}
			root = filepath.Join(cache, "ivp", "artifacts")
		}
		return artifact.NewFileStore(root)
	case "s3":
		allowInsecure := strings.EqualFold(strings.TrimSpace(os.Getenv("IVP_S3_ALLOW_INSECURE")), "true")
		return artifact.NewS3Store(artifact.S3Config{Endpoint: os.Getenv("IVP_S3_ENDPOINT"), Region: os.Getenv("IVP_S3_REGION"), Bucket: os.Getenv("IVP_S3_BUCKET"), AccessKey: os.Getenv("IVP_S3_ACCESS_KEY"), SecretKey: os.Getenv("IVP_S3_SECRET_KEY"), SessionToken: os.Getenv("IVP_S3_SESSION_TOKEN"), AllowInsecure: allowInsecure})
	default:
		return nil, errors.New("IVP_ARTIFACT_BACKEND must be filesystem or s3")
	}
}

func retentionDays(raw string) (int, error) {
	if strings.TrimSpace(raw) == "" {
		return 90, nil
	}
	days, err := strconv.Atoi(raw)
	if err != nil || days < 1 || days > 36500 {
		return 0, errors.New("IVP_RAW_HISTORY_RETENTION_DAYS must be an integer between 1 and 36500")
	}
	return days, nil
}

func developmentAuthEnabled() bool {
	return strings.EqualFold(os.Getenv("IVP_ENV"), "development") && os.Getenv("IVP_DEV_AUTH") == "1"
}

func ensureDevelopmentIdentity(ctx context.Context, db *persistence.DB, tenantID, userID, subject string) error {
	tenant, err := uuid.Parse(tenantID)
	if err != nil {
		return errors.New("IVP_DEV_TENANT_ID must be a UUID")
	}
	user, err := uuid.Parse(userID)
	if err != nil {
		return errors.New("IVP_DEV_USER_ID must be a UUID")
	}
	if strings.TrimSpace(subject) == "" || len(subject) > 200 {
		return errors.New("IVP_DEV_SUBJECT must contain 1 to 200 characters")
	}
	if _, err := db.Pool.Exec(ctx, `INSERT INTO tenants(id,name) VALUES($1,'Local development') ON CONFLICT(id) DO NOTHING`, tenant); err != nil {
		return err
	}
	if _, err := db.Pool.Exec(ctx, `INSERT INTO users(id,oidc_subject) VALUES($1,$2) ON CONFLICT(oidc_subject) DO NOTHING`, user, subject); err != nil {
		return err
	}
	var storedSubject string
	if err := db.Pool.QueryRow(ctx, `SELECT oidc_subject FROM users WHERE id=$1`, user).Scan(&storedSubject); err != nil {
		return err
	}
	if storedSubject != subject {
		return errors.New("development user id is already assigned to a different subject")
	}
	return db.TenantTx(ctx, tenant.String(), func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `INSERT INTO tenant_memberships(tenant_id,user_id,role) VALUES($1,$2,'admin') ON CONFLICT(tenant_id,user_id) DO UPDATE SET role='admin'`, tenant, user)
		return err
	})
}
