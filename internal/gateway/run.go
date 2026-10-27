// SPDX-License-Identifier: Apache-2.0

// Package gateway routes users to their desktops (docs/contract.md, section 7).
package gateway

import (
	"context"
	"errors"

	"k8s.io/client-go/rest"

	"github.com/lhns/kube-webtop-workspaces/internal/config"
)

// Run starts the gateway. It blocks until ctx is done.
func Run(ctx context.Context, restCfg *rest.Config, cfg *config.Config) error {
	return errors.New("gateway: not implemented")
}
