// SPDX-License-Identifier: Apache-2.0

// Package deskctl implements the admin commands of `kube-webtop-workspaces desktops`.
// It never touches a PVC: contract.md forbids the controller from deleting one.
package deskctl

import (
	"context"
	"fmt"
	"io"
	"sort"
	"text/tabwriter"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/lhns/kube-webtop-workspaces/api/v1alpha1"
	"github.com/lhns/kube-webtop-workspaces/internal/config"
	"github.com/lhns/kube-webtop-workspaces/internal/lifecycle"
	"github.com/lhns/kube-webtop-workspaces/internal/names"
	"github.com/lhns/kube-webtop-workspaces/internal/render"
)

type CLI struct {
	Client client.Client
	Config *config.Config
	Out    io.Writer
	Now    func() time.Time
}

func (c *CLI) now() time.Time {
	if c.Now != nil {
		return c.Now()
	}
	return time.Now()
}

// find resolves a user ID (or a desktop name) to its Desktop.
func (c *CLI) find(ctx context.Context, user string) (*v1alpha1.Desktop, error) {
	candidates := []string{user}
	if n, err := names.Sanitize(user); err == nil && n != user {
		candidates = []string{n, user}
	}
	for _, n := range candidates {
		d := &v1alpha1.Desktop{}
		err := c.Client.Get(ctx, client.ObjectKey{Namespace: c.Config.Namespace, Name: n}, d)
		if err == nil {
			return d, nil
		}
		if !apierrors.IsNotFound(err) {
			return nil, err
		}
	}
	return nil, fmt.Errorf("no desktop for %q", user)
}

// List prints one line per desktop.
func (c *CLI) List(ctx context.Context) error {
	var list v1alpha1.DesktopList
	if err := c.Client.List(ctx, &list, client.InNamespace(c.Config.Namespace)); err != nil {
		return err
	}
	sort.Slice(list.Items, func(i, j int) bool { return list.Items[i].Spec.User < list.Items[j].Spec.User })
	w := tabwriter.NewWriter(c.Out, 0, 4, 2, ' ', 0)
	fmt.Fprintln(w, "USER\tPOWER\tPHASE\tLAST SEEN\tCLIENTS\tPVC")
	for i := range list.Items {
		d := &list.Items[i]
		seen := "-"
		if d.Spec.LastSeen != nil {
			seen = d.Spec.LastSeen.UTC().Format(time.RFC3339)
		}
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%d\t%s\n", d.Spec.User, d.Spec.PowerState, d.Status.Phase, seen,
			d.Status.ConnectedClients, render.PVC(c.Config, d).Name)
	}
	return w.Flush()
}

// Stop sets the desktop Off with reason admin. Data stays.
func (c *CLI) Stop(ctx context.Context, user string) error {
	d, err := c.find(ctx, user)
	if err != nil {
		return err
	}
	if err := lifecycle.Stop(ctx, c.Client, d, v1alpha1.StopAdmin, c.now()); err != nil {
		return err
	}
	fmt.Fprintf(c.Out, "stopped %s; its data is kept (PVC %s)\n", d.Spec.User, render.PVC(c.Config, d).Name)
	return nil
}

// Remove deletes the Desktop (the Deployment and Service follow; the PVC stays).
func (c *CLI) Remove(ctx context.Context, user string) error {
	d, err := c.find(ctx, user)
	if err != nil {
		return err
	}
	if err := c.Client.Delete(ctx, d); err != nil {
		return err
	}
	pvc := render.PVC(c.Config, d).Name
	fmt.Fprintf(c.Out, "removed the desktop of %s. The PVC %s is kept; delete it (and its volume) by hand once the data is not wanted.\n", d.Spec.User, pvc)
	fmt.Fprintln(c.Out, "A user who is still permitted gets a new desktop on the next visit, which reuses that PVC.")
	return nil
}
