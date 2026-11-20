package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"syscall"
	"time"

	"github.com/spf13/cobra"

	"github.com/dustywusty/yak/internal/domain"
	"github.com/dustywusty/yak/internal/httpapi"
	"github.com/dustywusty/yak/internal/postgres"
	"github.com/dustywusty/yak/internal/workers"
)

// Server-side commands. These talk to Postgres directly and are operator
// tools for the reference server; they are not part of the client protocol.

var version = "0.1.0-dev"

func databaseURL() (string, error) {
	u := yakEnv("YAK_DATABASE_URL")
	if u == "" {
		return "", errors.New("YAK_DATABASE_URL is required")
	}
	return u, nil
}

func serverCmds() []*cobra.Command {
	serve := &cobra.Command{
		Use:   "serve",
		Short: "Run the Yak reference server (API and/or worker roles)",
		RunE: func(cmd *cobra.Command, args []string) error {
			listen, _ := cmd.Flags().GetString("listen")
			role, _ := cmd.Flags().GetString("role")
			dbURL, err := databaseURL()
			if err != nil {
				return err
			}
			// Timestamps read from Postgres decode in time.Local; the protocol
			// requires UTC (WIRE-003), so the server process runs in UTC.
			time.Local = time.UTC
			log := slog.New(slog.NewJSONHandler(os.Stderr, nil))
			ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
			defer stop()
			pool, err := postgres.Connect(ctx, dbURL)
			if err != nil {
				return err
			}
			defer pool.Close()
			if v, err := postgres.CheckSchema(ctx, pool); err != nil {
				return fmt.Errorf("schema version %d: %w", v, err)
			}
			cfg := domain.DefaultConfig()
			cfg.Version = version
			if v := yakEnv("YAK_CLAIM_LEASE_MIN_SECONDS"); v != "" {
				n, err := strconv.Atoi(v)
				if err != nil || n < 1 {
					return fmt.Errorf("YAK_CLAIM_LEASE_MIN_SECONDS must be a positive integer")
				}
				cfg.ClaimLeaseMin = time.Duration(n) * time.Second
			}
			svc := domain.New(pool, cfg)
			// Claim-proof replay keys (ADR-0004 §3): never stored in the
			// database. YAK_CLAIM_REPLAY_KEYS is a keyring "id:base64,...";
			// YAK_CLAIM_PROOF_KEY is a single base64 key. Without either the
			// server advertises no claim-proof replay (CLAIM-043).
			spec := yakEnv("YAK_CLAIM_REPLAY_KEYS")
			if spec == "" {
				spec = yakEnv("YAK_CLAIM_PROOF_KEY")
			}
			if svc.ClaimKeys, err = domain.ParseClaimKeyring(spec); err != nil {
				return fmt.Errorf("claim replay key: %w", err)
			}
			if role == "all" || role == "worker" {
				go workers.Run(ctx, svc, log, time.Minute)
				go workers.RunClaimExpiry(ctx, svc, log, 10*time.Second)
			}
			if role == "worker" {
				<-ctx.Done()
				return nil
			}
			go svc.Hub.Listen(ctx, dbURL, log)
			srv := &http.Server{Addr: listen, Handler: httpapi.New(svc, log), ReadHeaderTimeout: 10 * time.Second,
				// Bodies are at most 1 MiB; long polls last at most 30 s.
				ReadTimeout: 30 * time.Second, WriteTimeout: 45 * time.Second, IdleTimeout: 120 * time.Second}
			go func() {
				<-ctx.Done()
				sctx, cancel := context.WithTimeout(context.Background(), 35*time.Second)
				defer cancel()
				_ = srv.Shutdown(sctx)
			}()
			log.Info("yak listening", "addr", listen, "role", role, "version", version)
			if err := srv.ListenAndServe(); !errors.Is(err, http.ErrServerClosed) {
				return err
			}
			return nil
		},
	}
	serve.Flags().String("listen", envOr("YAK_LISTEN", ":8080"), "listen address")
	serve.Flags().String("role", envOr("YAK_ROLE", "all"), "all | api | worker")

	migrate := &cobra.Command{
		Use:   "migrate",
		Short: "Apply database migrations (serialized by an advisory lock)",
		RunE: func(cmd *cobra.Command, args []string) error {
			dbURL, err := databaseURL()
			if err != nil {
				return err
			}
			applied, err := postgres.Migrate(cmd.Context(), dbURL)
			for _, a := range applied {
				fmt.Println("applied", a)
			}
			if err == nil && len(applied) == 0 {
				fmt.Println("schema up to date")
			}
			return err
		},
	}

	admin := &cobra.Command{Use: "admin", Short: "Operator commands executed against the database"}
	boot := &cobra.Command{
		Use:   "bootstrap",
		Short: "Create the first server administrator and print its token once",
		RunE: func(cmd *cobra.Command, args []string) error {
			handle, _ := cmd.Flags().GetString("handle")
			display, _ := cmd.Flags().GetString("display-name")
			ttl, _ := cmd.Flags().GetDuration("ttl")
			dbURL, err := databaseURL()
			if err != nil {
				return err
			}
			pool, err := postgres.Connect(cmd.Context(), dbURL)
			if err != nil {
				return err
			}
			defer pool.Close()
			if _, err := postgres.CheckSchema(cmd.Context(), pool); err != nil {
				return err
			}
			if display == "" {
				display = handle
			}
			svc := domain.New(pool, domain.DefaultConfig())
			a, ic, err := svc.Bootstrap(cmd.Context(), handle, display, ttl)
			if err != nil {
				return err
			}
			fmt.Fprintf(os.Stderr, "created server administrator %s (%s); credential %s expires %s\n", a.Handle, a.ID, ic.Credential.ID, ic.Credential.ExpiresAt.Format(time.RFC3339))
			fmt.Fprintln(os.Stderr, "the token below is shown once; store it outside Yak (it is a recovery credential)")
			fmt.Println(ic.Token)
			return nil
		},
	}
	boot.Flags().String("handle", "admin", "administrator handle")
	boot.Flags().String("display-name", "", "display name")
	boot.Flags().Duration("ttl", 90*24*time.Hour, "credential lifetime")
	rotate := &cobra.Command{
		Use:   "rotate-epoch",
		Short: "Assign a new history epoch (run after restoring a backup)",
		RunE: func(cmd *cobra.Command, args []string) error {
			dbURL, err := databaseURL()
			if err != nil {
				return err
			}
			pool, err := postgres.Connect(cmd.Context(), dbURL)
			if err != nil {
				return err
			}
			defer pool.Close()
			e, err := postgres.RotateEpoch(cmd.Context(), pool)
			if err != nil {
				return err
			}
			fmt.Println("history epoch is now", e)
			return nil
		},
	}
	admin.AddCommand(boot, rotate)
	return []*cobra.Command{serve, migrate, admin}
}

func envOr(k, d string) string {
	if v := yakEnv(k); v != "" {
		return v
	}
	return d
}
