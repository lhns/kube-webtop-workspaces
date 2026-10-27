// SPDX-License-Identifier: Apache-2.0

// Command kube-webtop-workspaces runs the gateway or the controller (docs/contract.md).
package main

import (
	"fmt"
	"log/slog"
	"os"

	"github.com/go-logr/logr"
	"github.com/spf13/cobra"
	"k8s.io/client-go/tools/clientcmd"
	ctrl "sigs.k8s.io/controller-runtime"

	"github.com/lhns/kube-webtop-workspaces/internal/config"
	"github.com/lhns/kube-webtop-workspaces/internal/controller"
	"github.com/lhns/kube-webtop-workspaces/internal/gateway"
)

func main() {
	var mode, configPath, kubeconfig, logLevel, logFormat string
	cmd := &cobra.Command{
		Use:          "kube-webtop-workspaces",
		Short:        "Per-user persistent desktops on Kubernetes: gateway and controller",
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if err := setupLogging(logLevel, logFormat); err != nil {
				return err
			}
			cfg, err := config.Load(configPath)
			if err != nil {
				return err
			}
			restCfg, err := ctrl.GetConfig()
			if kubeconfig != "" {
				restCfg, err = clientcmd.BuildConfigFromFlags("", kubeconfig)
			}
			if err != nil {
				return err
			}
			ctx := ctrl.SetupSignalHandler()
			switch mode {
			case "controller":
				if err := cfg.ValidateController(); err != nil {
					return err
				}
				return controller.Run(ctx, restCfg, cfg)
			case "gateway":
				return gateway.Run(ctx, restCfg, cfg)
			default:
				return fmt.Errorf("--mode must be gateway or controller, got %q", mode)
			}
		},
	}
	f := cmd.Flags()
	f.StringVar(&mode, "mode", "", "gateway or controller")
	f.StringVar(&configPath, "config", "/etc/kube-webtop-workspaces/config.yaml", "config file")
	f.StringVar(&kubeconfig, "kubeconfig", "", "kubeconfig (default: in-cluster or KUBECONFIG)")
	f.StringVar(&logLevel, "log-level", "info", "debug, info, warn or error")
	f.StringVar(&logFormat, "log-format", "json", "json or text")
	_ = cmd.MarkFlagRequired("mode")
	if err := cmd.Execute(); err != nil {
		os.Exit(1)
	}
}

func setupLogging(level, format string) error {
	var lv slog.Level
	if err := lv.UnmarshalText([]byte(level)); err != nil {
		return fmt.Errorf("--log-level: %w", err)
	}
	opts := &slog.HandlerOptions{Level: lv}
	var h slog.Handler
	switch format {
	case "json":
		h = slog.NewJSONHandler(os.Stderr, opts)
	case "text":
		h = slog.NewTextHandler(os.Stderr, opts)
	default:
		return fmt.Errorf("--log-format must be json or text, got %q", format)
	}
	slog.SetDefault(slog.New(h))
	ctrl.SetLogger(logr.FromSlogHandler(h))
	return nil
}
