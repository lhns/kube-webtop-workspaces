// SPDX-License-Identifier: Apache-2.0

package controller

import (
	"context"
	"errors"
	"log/slog"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/lhns/kube-webtop-workspaces/api/v1alpha1"
	"github.com/lhns/kube-webtop-workspaces/internal/lifecycle"
)

// UnseenSweepInterval is how often the sweep runs; stopAfterUnseen is days, so this is not tight.
const UnseenSweepInterval = 10 * time.Minute

// UnseenSweep stops desktops whose owner the gateway has not served for After
// (desktop.stopAfterUnseen). Leader only. Connected clients do not exempt a desktop: an open tab
// of a removed user is exactly the case. Data is kept; a permitted user starts it again.
type UnseenSweep struct {
	Client    client.Client
	Namespace string
	// Gateway is the gateway Deployment; while it has no ready pod nobody can be seen.
	Gateway  string
	After    time.Duration
	Interval time.Duration
	Now      func() time.Time
}

func (s *UnseenSweep) NeedLeaderElection() bool { return true }

// Start sweeps now and then every Interval until ctx is done.
func (s *UnseenSweep) Start(ctx context.Context) error {
	t := time.NewTicker(s.Interval)
	defer t.Stop()
	for {
		if err := s.SweepOnce(ctx); err != nil {
			slog.Error("unseen sweep failed", "err", err)
		}
		select {
		case <-ctx.Done():
			return nil
		case <-t.C:
		}
	}
}

// SweepOnce stamps desktops that have no lastSeen yet (existing ones after an upgrade are never
// expired on the spot) and stops the expired ones, unless the gateway is down.
func (s *UnseenSweep) SweepOnce(ctx context.Context) error {
	now := time.Now
	if s.Now != nil {
		now = s.Now
	}
	var list v1alpha1.DesktopList
	if err := s.Client.List(ctx, &list, client.InNamespace(s.Namespace)); err != nil {
		return err
	}
	var errs []error
	for i := range list.Items {
		d := &list.Items[i]
		if d.Spec.LastSeen != nil {
			continue
		}
		orig := d.DeepCopy()
		t := metav1.NewTime(now())
		d.Spec.LastSeen = &t
		if err := s.Client.Patch(ctx, d, client.MergeFrom(orig)); err != nil {
			errs = append(errs, err)
		}
	}
	if !s.gatewayUp(ctx) {
		slog.Warn("unseen sweep skipped: the gateway has no ready pod")
		return errors.Join(errs...)
	}
	for i := range list.Items {
		d := &list.Items[i]
		if d.Spec.PowerState != v1alpha1.PowerOn || d.Spec.LastSeen == nil || now().Sub(d.Spec.LastSeen.Time) <= s.After {
			continue
		}
		if err := lifecycle.Stop(ctx, s.Client, d, v1alpha1.StopUnseen, now()); err != nil {
			errs = append(errs, err)
			continue
		}
		slog.Info("stopped a desktop its owner has not used", "desktop", d.Name, "lastSeen", d.Spec.LastSeen.Time)
	}
	return errors.Join(errs...)
}

func (s *UnseenSweep) gatewayUp(ctx context.Context) bool {
	var dep appsv1.Deployment
	if err := s.Client.Get(ctx, client.ObjectKey{Namespace: s.Namespace, Name: s.Gateway}, &dep); err != nil {
		slog.Warn("unseen sweep: reading the gateway Deployment", "err", err)
		return false
	}
	return dep.Status.ReadyReplicas > 0
}
