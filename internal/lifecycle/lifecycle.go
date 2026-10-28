// SPDX-License-Identifier: Apache-2.0

// Package lifecycle records desktop stops; the reconciler, the LDAP sync and the power API share it.
package lifecycle

import (
	"context"
	"fmt"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/lhns/kube-webtop-workspaces/api/v1alpha1"
	"github.com/lhns/kube-webtop-workspaces/internal/metrics"
)

// StopRecorded reports whether a stop since the last start already has a reason.
func StopRecorded(d *v1alpha1.Desktop) bool {
	ls := d.Status.LastStop
	return ls != nil && (d.Status.LastStart == nil || !ls.Time.Before(d.Status.LastStart))
}

// Stop records the stop reason (unless one is on record) and then sets powerState Off, in that
// order, so a reader of Off always finds the reason. Calling it again is a no-op.
func Stop(ctx context.Context, c client.Client, d *v1alpha1.Desktop, reason v1alpha1.StopReason, now time.Time) error {
	if !StopRecorded(d) {
		orig := d.DeepCopy()
		d.Status.LastStop = &v1alpha1.LastStop{Time: metav1.NewMicroTime(now), Reason: reason}
		if err := c.Status().Patch(ctx, d, client.MergeFrom(orig)); err != nil {
			return fmt.Errorf("record stop: %w", err)
		}
		metrics.StopsTotal.WithLabelValues(string(reason)).Inc()
	}
	if d.Spec.PowerState != v1alpha1.PowerOff {
		orig := d.DeepCopy()
		d.Spec.PowerState = v1alpha1.PowerOff
		if err := c.Patch(ctx, d, client.MergeFrom(orig)); err != nil {
			return fmt.Errorf("set powerState Off: %w", err)
		}
	}
	return nil
}
