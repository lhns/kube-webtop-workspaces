// SPDX-License-Identifier: Apache-2.0

package gateway

import (
	"context"

	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/lhns/kube-webtop-workspaces/api/v1alpha1"
)

// Store is the gateway's whole view of the cluster: read, create, and set spec.powerState=On.
type Store interface {
	Get(ctx context.Context, name string) (*v1alpha1.Desktop, error)
	Create(ctx context.Context, d *v1alpha1.Desktop) error
	PowerOn(ctx context.Context, d *v1alpha1.Desktop) error
}

// clientStore implements Store on a controller-runtime client (reads from the cache in Run).
type clientStore struct {
	c  client.Client
	ns string
}

func (s clientStore) Get(ctx context.Context, name string) (*v1alpha1.Desktop, error) {
	d := &v1alpha1.Desktop{}
	if err := s.c.Get(ctx, client.ObjectKey{Namespace: s.ns, Name: name}, d); err != nil {
		return nil, err
	}
	return d, nil
}

func (s clientStore) Create(ctx context.Context, d *v1alpha1.Desktop) error {
	return s.c.Create(ctx, d)
}

// PowerOn merge-patches only spec.powerState, so it never overwrites other spec changes.
func (s clientStore) PowerOn(ctx context.Context, d *v1alpha1.Desktop) error {
	obj := d.DeepCopy()
	obj.Spec.PowerState = v1alpha1.PowerOn
	return s.c.Patch(ctx, obj, client.MergeFrom(d))
}
