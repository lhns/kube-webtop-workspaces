// SPDX-License-Identifier: Apache-2.0

// Package controller reconciles Desktops (docs/contract.md, section 3).
package controller

import (
	"context"
	"fmt"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/fields"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/rest"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/cache"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/healthz"
	crmetrics "sigs.k8s.io/controller-runtime/pkg/metrics"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"

	"github.com/lhns/kube-webtop-workspaces/api/v1alpha1"
	"github.com/lhns/kube-webtop-workspaces/internal/config"
	"github.com/lhns/kube-webtop-workspaces/internal/ldap"
	"github.com/lhns/kube-webtop-workspaces/internal/names"
	"github.com/lhns/kube-webtop-workspaces/internal/powerapi"
)

// Scheme has the core types and the Desktop API.
func Scheme() (*runtime.Scheme, error) {
	s := runtime.NewScheme()
	if err := clientgoscheme.AddToScheme(s); err != nil {
		return nil, err
	}
	if err := v1alpha1.AddToScheme(s); err != nil {
		return nil, err
	}
	return s, nil
}

// CacheOptions restricts the cache to the namespace, pods to the desktop pods and events to pod events.
func CacheOptions(cfg *config.Config) cache.Options {
	return cache.Options{
		DefaultNamespaces: map[string]cache.Config{cfg.Namespace: {}},
		ByObject: map[client.Object]cache.ByObject{
			&corev1.Pod{}: {Label: labels.SelectorFromSet(labels.Set{names.LabelManagedBy: names.ManagedBy})},
			// Only Pod events are listed and watched; the reconciler reads them by pod UID.
			&corev1.Event{}: {Field: fields.SelectorFromSet(fields.Set{"involvedObject.kind": "Pod"})},
		},
	}
}

// Setup adds the reconciler and, when dir is not nil, the LDAP sync to mgr.
func Setup(mgr ctrl.Manager, cfg *config.Config, dir ldap.Directory) (*Reconciler, *LDAPSync, error) {
	notify := make(chan event.GenericEvent, 256)
	members := &MemberCache{}
	r := &Reconciler{
		Client: mgr.GetClient(), APIReader: mgr.GetAPIReader(),
		Dir: dir, Members: members, Notify: notify,
	}
	r.SetConfig(cfg)
	if err := r.SetupWithManager(mgr); err != nil {
		return nil, nil, err
	}
	if dir == nil {
		return r, nil, nil
	}
	s := &LDAPSync{
		Client: mgr.GetClient(), Dir: dir, Cache: members, Namespace: cfg.Namespace,
		Interval: cfg.LDAP.SyncInterval.Duration, Notify: notify,
	}
	if err := mgr.Add(s); err != nil {
		return nil, nil, err
	}
	return r, s, nil
}

// Run starts the controller manager: reconciler and LDAP sync (leader only) and the
// power/report API (every replica). It blocks until ctx is done.
func Run(ctx context.Context, restCfg *rest.Config, cfg *config.Config) error {
	scheme, err := Scheme()
	if err != nil {
		return err
	}
	mgr, err := ctrl.NewManager(restCfg, ctrl.Options{
		Scheme:                        scheme,
		Cache:                         CacheOptions(cfg),
		Metrics:                       metricsserver.Options{BindAddress: cfg.MetricsAddr},
		HealthProbeBindAddress:        cfg.HealthAddr,
		LeaderElection:                *cfg.Controller.LeaderElection,
		LeaderElectionID:              cfg.Controller.LeaderElectionID,
		LeaderElectionNamespace:       cfg.Namespace,
		LeaderElectionReleaseOnCancel: true,
	})
	if err != nil {
		return fmt.Errorf("new manager: %w", err)
	}
	if err := mgr.AddHealthzCheck("healthz", healthz.Ping); err != nil {
		return err
	}
	if err := mgr.AddReadyzCheck("readyz", healthz.Ping); err != nil {
		return err
	}

	var dir ldap.Directory
	if cfg.LDAP.Enabled() {
		dir = ldap.New(cfg.LDAP)
	}
	if _, _, err := Setup(mgr, cfg, dir); err != nil {
		return err
	}

	api := &powerapi.Server{
		Client: mgr.GetClient(), Reader: mgr.GetAPIReader(),
		Auth:      &powerapi.TokenReviewer{Client: mgr.GetClient(), Audience: cfg.PowerAPI.Audience},
		Namespace: cfg.Namespace, ServiceAccount: cfg.Desktop.ServiceAccountName, Audience: cfg.PowerAPI.Audience,
	}
	if err := mgr.Add(powerapi.Runnable{Addr: cfg.PowerAPI.Addr, Handler: api.Handler()}); err != nil {
		return err
	}

	if err := crmetrics.Registry.Register(&Collector{Reader: mgr.GetCache(), Elected: mgr.Elected(), Namespace: cfg.Namespace}); err != nil {
		return err
	}
	return mgr.Start(ctx)
}
