// SPDX-License-Identifier: Apache-2.0

package controller

import (
	"context"
	"errors"
	"log/slog"
	"sync"
	"time"

	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/event"

	"github.com/lhns/kube-webtop-workspaces/api/v1alpha1"
	"github.com/lhns/kube-webtop-workspaces/internal/ldap"
	"github.com/lhns/kube-webtop-workspaces/internal/metrics"
)

// MemberCache is the in-memory set of group members, filled by the LDAP sync.
type MemberCache struct {
	mu     sync.RWMutex
	loaded bool
	set    map[string]struct{}
}

// Contains reports membership and whether a successful sync has filled the cache yet.
func (c *MemberCache) Contains(user string) (member, loaded bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	_, member = c.set[user]
	return member, c.loaded
}

// Replace swaps in a new member set.
func (c *MemberCache) Replace(set map[string]struct{}) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.set, c.loaded = set, true
}

// Add records a member confirmed by a direct lookup; the next sync replaces the set.
func (c *MemberCache) Add(user string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.set == nil {
		c.set = map[string]struct{}{}
	}
	c.set[user] = struct{}{}
}

// LDAPSync refreshes the member cache and stops desktops of removed users. Leader only.
type LDAPSync struct {
	Client    client.Client
	Dir       ldap.Directory
	Cache     *MemberCache
	Namespace string
	Interval  time.Duration
	// Notify makes the reconciler look at every Desktop after a successful sync.
	Notify chan<- event.GenericEvent
	Now    func() time.Time
}

func (s *LDAPSync) NeedLeaderElection() bool { return true }

// Start runs a sync now and then every Interval until ctx is done.
func (s *LDAPSync) Start(ctx context.Context) error {
	t := time.NewTicker(s.Interval)
	defer t.Stop()
	for {
		if err := s.SyncOnce(ctx); err != nil {
			slog.Error("ldap sync failed; nothing changed", "err", err)
		}
		select {
		case <-ctx.Done():
			return nil
		case <-t.C:
		}
	}
}

// SyncOnce reads the group. On error, or an empty group, nothing changes.
func (s *LDAPSync) SyncOnce(ctx context.Context) error {
	members, err := s.Dir.Members(ctx)
	if err == nil && len(members) == 0 {
		err = ldap.ErrNoMembers
	}
	if err != nil {
		metrics.LDAPSyncs.WithLabelValues("error").Inc()
		return err
	}
	s.Cache.Replace(members)
	metrics.LDAPSyncs.WithLabelValues("success").Inc()
	metrics.LDAPLastSuccess.SetToCurrentTime()
	metrics.LDAPMembers.Set(float64(len(members)))

	var list v1alpha1.DesktopList
	if err := s.Client.List(ctx, &list, client.InNamespace(s.Namespace)); err != nil {
		return err
	}
	now := time.Now
	if s.Now != nil {
		now = s.Now
	}
	var errs []error
	for i := range list.Items {
		d := &list.Items[i]
		if _, ok := members[d.Spec.User]; !ok && d.Spec.PowerState == v1alpha1.PowerOn {
			if err := stopDesktop(ctx, s.Client, d, v1alpha1.StopLDAP, now()); err != nil {
				errs = append(errs, err)
				continue
			}
			slog.Info("stopped desktop of a user who left the group", "desktop", d.Name)
		}
		if s.Notify != nil {
			select {
			case s.Notify <- event.GenericEvent{Object: d}:
			case <-ctx.Done():
				return ctx.Err()
			}
		}
	}
	return errors.Join(errs...)
}
