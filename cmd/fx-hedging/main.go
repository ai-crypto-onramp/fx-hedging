package main

import (
	"context"
	"log"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"syscall"
	"time"

	"github.com/ai-crypto-onramp/fx-hedging/internal/api"
	"github.com/ai-crypto-onramp/fx-hedging/internal/audit"
	"github.com/ai-crypto-onramp/fx-hedging/internal/clients"
	"github.com/ai-crypto-onramp/fx-hedging/internal/executor"
	"github.com/ai-crypto-onramp/fx-hedging/internal/exposure"
	grpcserver "github.com/ai-crypto-onramp/fx-hedging/internal/grpc"
	"github.com/ai-crypto-onramp/fx-hedging/internal/policy"
	"github.com/ai-crypto-onramp/fx-hedging/internal/provider"
	"github.com/ai-crypto-onramp/fx-hedging/internal/ratecache"
	"github.com/ai-crypto-onramp/fx-hedging/internal/store"
	"github.com/ai-crypto-onramp/fx-hedging/internal/store/postgres"
)

func main() {
	log.Fatal(run())
}

// run starts the REST and gRPC servers and blocks until a signal is
// received or a server fails.
func run() error {
	port := os.Getenv("PORT")
	if port == "" {
		port = "8080"
	}
	grpcPort := os.Getenv("GRPC_PORT")
	if grpcPort == "" {
		grpcPort = "9090"
	}

	svc := newService()

	// Start the exposure snapshotter (persists snapshots on change + tick).
	interval := 1000
	if v := os.Getenv("EXPOSURE_REFRESH_INTERVAL_MS"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			interval = n
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	snap := exposure.NewSnapshotter(svc.Tracker, svc.Store, time.Duration(interval)*time.Millisecond)
	go snap.Run(ctx)

	// Start gRPC server (Treasury / Pricing integration).
	gsrv, lis, err := grpcserver.NewServer(&grpcserver.Services{
		Tracker: svc.Tracker,
		Cache:   svc.Cache,
		Policy:  svc.Policy,
		Router:  executor.NewRouter(executor.NewLatencyExecutor(executor.NewBankAdapter(1.10), 500*time.Millisecond)),
		Store:   svc.Store,
	}, grpcPort)
	if err != nil {
		log.Printf("grpc: start failed: %v (continuing with REST only)", err)
	} else {
		go func() {
			log.Printf("grpc: listening on :%s", grpcPort)
			if err := gsrv.Serve(lis); err != nil {
				log.Printf("grpc: serve: %v", err)
			}
		}()
	}

	// REST server.
	httpSrv := &http.Server{Addr: ":" + port, Handler: api.NewMux(svc)}
	go func() {
		log.Printf("rest: listening on :%s", port)
		if err := httpSrv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Printf("rest: serve: %v", err)
			cancel()
		}
	}()

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	<-sigCh
	log.Printf("shutdown: signal received")
	shutdownCtx, sCancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer sCancel()
	_ = httpSrv.Shutdown(shutdownCtx)
	if gsrv != nil {
		gsrv.GracefulStop()
	}
	cancel()
	return nil
}

// newService wires the service dependencies from environment configuration.
func newService() *api.Service {
	ctx := context.Background()
	devMode := os.Getenv("DEV_MODE") == "1"
	var st store.Store
	if dsn := os.Getenv("DB_URL"); dsn != "" {
		db, err := postgres.Open(ctx, dsn)
		if err != nil {
			log.Fatalf("postgres: open: %v", err)
		}
		st = db
	} else {
		if !devMode {
			log.Fatalf("DB_URL not set and DEV_MODE!=1; refusing to start in production mode")
		}
		log.Printf("DEV_MODE=1: DB_URL unset — using in-memory store (NOT FOR PRODUCTION)")
		st = store.New()
	}
	var p provider.FXProvider
	if devMode {
		log.Printf("DEV_MODE=1: using stub FX provider (NOT FOR PRODUCTION)")
		p = provider.NewDummy()
	} else {
		// Real FX provider client not yet implemented; require FX_PROVIDER_URL.
		if os.Getenv("FX_PROVIDER_URL") == "" {
			log.Fatalf("FX_PROVIDER_URL required in production mode; real FX provider client not yet implemented — set DEV_MODE=1 for local dev")
		}
		// Unreachable until a real client is wired; fall through to dummy so
		// the compile shape stays stable. The fatal above guards prod.
		p = provider.NewDummy()
	}
	tr := exposure.New()
	pol := policy.New()
	rec := audit.NewRecorder()
	svc := api.NewService(st, tr, p, pol, rec)

	// Wire downstream clients (audit-event-log + reconciliation) with a
	// shared in-memory dead-letter store. Empty URLs degrade to no-ops.
	dl := clients.NewMemDeadLetter()
	svc.AuditC = clients.NewAuditClient(dl)
	svc.ReconC = clients.NewReconClient(dl)

	// Wire the live rate cache (used by GetLiveRate gRPC).
	svc.Cache = ratecache.New(time.Second)

	return svc
}
