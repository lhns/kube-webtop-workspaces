// SPDX-License-Identifier: Apache-2.0

// Command kube-webtop-workspaces runs the gateway or the controller (docs/contract.md).
package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"

	"github.com/go-logr/logr"
	"github.com/spf13/cobra"
	"k8s.io/client-go/tools/clientcmd"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/lhns/kube-webtop-workspaces/internal/config"
	"github.com/lhns/kube-webtop-workspaces/internal/controller"
	"github.com/lhns/kube-webtop-workspaces/internal/deskctl"
	"github.com/lhns/kube-webtop-workspaces/internal/gateway"
	"github.com/lhns/kube-webtop-workspaces/internal/scheme"
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
	f := cmd.PersistentFlags()
	cmd.Flags().StringVar(&mode, "mode", "", "gateway or controller")
	f.StringVar(&configPath, "config", "/etc/kube-webtop-workspaces/config.yaml", "config file")
	f.StringVar(&kubeconfig, "kubeconfig", "", "kubeconfig (default: in-cluster or KUBECONFIG)")
	f.StringVar(&logLevel, "log-level", "info", "debug, info, warn or error")
	f.StringVar(&logFormat, "log-format", "json", "json or text")
	_ = cmd.MarkFlagRequired("mode")
	cmd.AddCommand(desktopsCommand(&configPath, &kubeconfig))
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

// desktopsCommand is the admin CLI. It runs in the controller pod with its credentials:
//
//	kubectl -n <ns> exec deploy/<release>-controller -- /kube-webtop-workspaces desktops list
func desktopsCommand(configPath, kubeconfig *string) *cobra.Command {
	root := &cobra.Command{Use: "desktops", Short: "List, stop and remove desktops", SilenceUsage: true}
	run := func(f func(*deskctl.CLI, context.Context, string) error, args int) func(*cobra.Command, []string) error {
		return func(cmd *cobra.Command, a []string) error {
			cfg, err := config.Load(*configPath)
			if err != nil {
				return err
			}
			restCfg, err := ctrl.GetConfig()
			if *kubeconfig != "" {
				restCfg, err = clientcmd.BuildConfigFromFlags("", *kubeconfig)
			}
			if err != nil {
				return err
			}
			sch, err := scheme.New()
			if err != nil {
				return err
			}
			c, err := client.New(restCfg, client.Options{Scheme: sch})
			if err != nil {
				return err
			}
			arg := ""
			if args > 0 {
				arg = a[0]
			}
			return f(&deskctl.CLI{Client: c, Config: cfg, Out: cmd.OutOrStdout()}, cmd.Context(), arg)
		}
	}
	root.AddCommand(
		&cobra.Command{Use: "list", Short: "User, power, phase, last seen, clients and PVC of every desktop", Args: cobra.NoArgs,
			RunE: run(func(c *deskctl.CLI, ctx context.Context, _ string) error { return c.List(ctx) }, 0)},
		&cobra.Command{Use: "stop <user>", Short: "Stop a desktop; its data is kept", Args: cobra.ExactArgs(1),
			RunE: run(func(c *deskctl.CLI, ctx context.Context, u string) error { return c.Stop(ctx, u) }, 1)},
		&cobra.Command{Use: "remove <user>", Short: "Delete the Desktop object; the PVC is kept", Args: cobra.ExactArgs(1),
			RunE: run(func(c *deskctl.CLI, ctx context.Context, u string) error { return c.Remove(ctx, u) }, 1)},
	)
	return root
}
