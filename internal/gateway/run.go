// SPDX-License-Identifier: Apache-2.0

// Package gateway routes users to their desktops (docs/contract.md, section 7).
package gateway

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"sync/atomic"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/collectors"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/rest"
	toolscache "k8s.io/client-go/tools/cache"
	"sigs.k8s.io/controller-runtime/pkg/cache"
	"sigs.k8s.io/controller-runtime/pkg/client"
	ctrllog "sigs.k8s.io/controller-runtime/pkg/log"

	"github.com/lhns/kube-webtop-workspaces/api/v1alpha1"
	"github.com/lhns/kube-webtop-workspaces/internal/config"
)

// Run starts the gateway. It blocks until ctx is done.
func Run(ctx context.Context, restCfg *rest.Config, cfg *config.Config) error {
	log := ctrllog.Log.WithName("gateway")

	scheme := runtime.NewScheme()
	if err := clientgoscheme.AddToScheme(scheme); err != nil {
		return err
	}
	if err := v1alpha1.AddToScheme(scheme); err != nil {
		return err
	}

	c, err := cache.New(restCfg, cache.Options{
		Scheme:            scheme,
		DefaultNamespaces: map[string]cache.Config{cfg.Namespace: {}},
	})
	if err != nil {
		return fmt.Errorf("cache: %w", err)
	}
	cl, err := client.New(restCfg, client.Options{
		Scheme: scheme,
		Cache:  &client.CacheOptions{Reader: c},
	})
	if err != nil {
		return fmt.Errorf("client: %w", err)
	}

	hub := NewHub()
	reg := prometheus.NewRegistry()
	reg.MustRegister(collectors.NewGoCollector(), collectors.NewProcessCollector(collectors.ProcessCollectorOpts{}))
	handler, err := NewHandler(Options{
		Config:    cfg.Gateway,
		Namespace: cfg.Namespace,
		Store:     clientStore{c: cl, ns: cfg.Namespace},
		Hub:       hub,
		Metrics:   NewMetrics(reg),
		Log:       log,
	})
	if err != nil {
		return err
	}

	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	cacheErr := make(chan error, 1)
	go func() { cacheErr <- c.Start(ctx) }()
	if !c.WaitForCacheSync(ctx) {
		cancel()
		if err := <-cacheErr; err != nil {
			return fmt.Errorf("cache: %w", err)
		}
		return errors.New("cache did not sync")
	}
	inf, err := c.GetInformer(ctx, &v1alpha1.Desktop{})
	if err != nil {
		return fmt.Errorf("desktop informer: %w", err)
	}
	notify := func(obj any) {
		if t, ok := obj.(toolscache.DeletedFinalStateUnknown); ok {
			obj = t.Obj
		}
		if o, ok := obj.(metav1.Object); ok {
			hub.Notify(o.GetName())
		}
	}
	if _, err := inf.AddEventHandler(toolscache.ResourceEventHandlerFuncs{
		AddFunc:    notify,
		UpdateFunc: func(_, obj any) { notify(obj) },
		DeleteFunc: notify,
	}); err != nil {
		return fmt.Errorf("desktop event handler: %w", err)
	}

	var ready atomic.Bool
	ready.Store(true)

	mainMux := http.NewServeMux()
	mainMux.Handle("/", handler)

	metricsMux := http.NewServeMux()
	metricsMux.Handle("/metrics", promhttp.HandlerFor(reg, promhttp.HandlerOpts{}))

	healthMux := http.NewServeMux()
	healthMux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })
	healthMux.HandleFunc("/readyz", func(w http.ResponseWriter, _ *http.Request) {
		if ready.Load() {
			w.WriteHeader(http.StatusOK)
			return
		}
		w.WriteHeader(http.StatusServiceUnavailable)
	})

	// The main server has no read/write timeouts: SSE and websockets are long-lived.
	servers := []*http.Server{
		{Addr: cfg.Gateway.Addr, Handler: mainMux, ReadHeaderTimeout: 10 * time.Second},
		{Addr: cfg.MetricsAddr, Handler: metricsMux, ReadHeaderTimeout: 10 * time.Second},
		{Addr: cfg.HealthAddr, Handler: healthMux, ReadHeaderTimeout: 10 * time.Second},
	}
	errs := make(chan error, len(servers)+1)
	for _, s := range servers {
		go func() {
			if err := s.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
				errs <- fmt.Errorf("serve %s: %w", s.Addr, err)
			}
		}()
	}
	log.Info("gateway started", "addr", cfg.Gateway.Addr, "namespace", cfg.Namespace)

	var runErr error
	select {
	case <-ctx.Done():
	case runErr = <-errs:
	case runErr = <-cacheErr:
		if runErr == nil {
			runErr = errors.New("cache stopped")
		}
	}
	cancel()

	ready.Store(false)
	shutdownCtx, stop := context.WithTimeout(context.Background(), 15*time.Second)
	defer stop()
	for _, s := range servers {
		if err := s.Shutdown(shutdownCtx); err != nil {
			// Long-lived streams outlive the grace period; drop them.
			_ = s.Close()
		}
	}
	return runErr
}
