// SPDX-License-Identifier: Apache-2.0

// Package controller reconciles Desktops (docs/contract.md, section 3).
package controller

import (
	"context"
	"errors"

	"k8s.io/client-go/rest"

	"github.com/lhns/kube-webtop-workspaces/internal/config"
)

// Run starts the controller manager: reconciler and LDAP sync (leader only) and the
// power/report API (every replica). It blocks until ctx is done.
func Run(ctx context.Context, restCfg *rest.Config, cfg *config.Config) error {
	return errors.New("controller: not implemented")
}
