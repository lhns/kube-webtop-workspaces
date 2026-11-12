// SPDX-License-Identifier: Apache-2.0

// Package config loads the YAML config shared by both modes (docs/contract.md, "Configuration").
package config

import (
	"errors"
	"fmt"
	"net/url"
	"os"
	"regexp"
	"strings"
	"text/template"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/yaml"

	"github.com/lhns/kube-webtop-workspaces/internal/names"
)

type Config struct {
	Namespace   string     `json:"namespace"`
	Names       Names      `json:"names"`
	MetricsAddr string     `json:"metricsAddr"`
	HealthAddr  string     `json:"healthAddr"`
	Controller  Controller `json:"controller"`
	PowerAPI    PowerAPI   `json:"powerApi"`
	Desktop     Desktop    `json:"desktop"`
	Storage     Storage    `json:"storage"`
	Gateway     Gateway    `json:"gateway"`

	titleTmpl *template.Template // desktop.title, parsed once by validate
}

type Names struct {
	Prefix string `json:"prefix"`
}

type Controller struct {
	LeaderElection    *bool   `json:"leaderElection"`
	LeaderElectionID  string  `json:"leaderElectionID"`
	RequireAgentReady *bool   `json:"requireAgentReady"`
	CleanExitCodes    []int32 `json:"cleanExitCodes"`
	// MaxConcurrentReconciles > 1 keeps one slow desktop from stalling the others.
	MaxConcurrentReconciles int `json:"maxConcurrentReconciles"`
	// GatewayDeployment is the gateway's Deployment; the unseen sweep waits while it has no ready pod.
	GatewayDeployment string `json:"gatewayDeployment"`
}

type PowerAPI struct {
	Addr     string `json:"addr"`
	Audience string `json:"audience"`
	URL      string `json:"url"`
}

type Desktop struct {
	Image                         string                        `json:"image"`
	ImagePullPolicy               corev1.PullPolicy             `json:"imagePullPolicy"`
	ImagePullSecrets              []corev1.LocalObjectReference `json:"imagePullSecrets"`
	Port                          int32                         `json:"port"`
	ServiceAccountName            string                        `json:"serviceAccountName"`
	RuntimeClassName              string                        `json:"runtimeClassName"`
	PriorityClassName             string                        `json:"priorityClassName"`
	HostUsers                     *bool                         `json:"hostUsers"`
	Resources                     corev1.ResourceRequirements   `json:"resources"`
	NodeSelector                  map[string]string             `json:"nodeSelector"`
	Tolerations                   []corev1.Toleration           `json:"tolerations"`
	Affinity                      *corev1.Affinity              `json:"affinity"`
	PodLabels                     map[string]string             `json:"podLabels"`
	PodAnnotations                map[string]string             `json:"podAnnotations"`
	ExtraEnv                      []corev1.EnvVar               `json:"extraEnv"`
	ExtraVolumes                  []corev1.Volume               `json:"extraVolumes"`
	ExtraVolumeMounts             []corev1.VolumeMount          `json:"extraVolumeMounts"`
	SecurityContext               *corev1.SecurityContext       `json:"securityContext"`
	ShmSize                       resource.Quantity             `json:"shmSize"`
	RunSizeLimit                  resource.Quantity             `json:"runSizeLimit"`
	TerminationGracePeriodSeconds *int64                        `json:"terminationGracePeriodSeconds"`
	// Selkies is SELKIES_* settings for every desktop (docs/contract.md, "Selkies settings").
	Selkies map[string]string `json:"selkies"`
	// Title is a text/template for the browser tab title; see TitleData. Empty: Selkies' own.
	Title string `json:"title"`
	// StopAfterUnseen stops a desktop whose owner the gateway has not served for this long.
	// 0 (the default) never stops one; otherwise at least MinStopAfterUnseen.
	StopAfterUnseen metav1.Duration `json:"stopAfterUnseen"`
	// TunDevice mounts the node's /dev/net/tun: rootless container networking (pasta) in the desktop.
	TunDevice bool `json:"tunDevice"`
	// SeccompProfile of the default container securityContext; nil: RuntimeDefault.
	SeccompProfile *corev1.SeccompProfile `json:"seccompProfile"`
}

// MinStopAfterUnseen keeps a login or gateway outage from reading as absent users.
const MinStopAfterUnseen = 24 * time.Hour

type Storage struct {
	StorageClassName string                              `json:"storageClassName"`
	Size             resource.Quantity                   `json:"size"`
	AccessModes      []corev1.PersistentVolumeAccessMode `json:"accessModes"`
	MountPath        string                              `json:"mountPath"`
	Labels           map[string]string                   `json:"labels"`
	Annotations      map[string]string                   `json:"annotations"`
}

type Gateway struct {
	Addr              string   `json:"addr"`
	UserHeader        string   `json:"userHeader"`
	GroupsHeader      string   `json:"groupsHeader"`
	GroupsSeparator   string   `json:"groupsSeparator"`
	AdminGroup        string   `json:"adminGroup"`
	RequiredGroup     string   `json:"requiredGroup"`
	TrustedProxyCIDRs []string `json:"trustedProxyCIDRs"`
	CookieSecure      *bool    `json:"cookieSecure"`
	// DisplayNameHeader carries the user's display name; empty keeps the last one.
	DisplayNameHeader string `json:"displayNameHeader"`
	// LogoutURL is where the browser goes when the session logs out; empty: nowhere.
	// It never appears in a page: the browser only visits a gateway path that redirects.
	LogoutURL string `json:"logoutUrl"`
	// LockLogsOut offers Lock in the desktop and makes it sign the browser out at the provider
	// (the session keeps running). It needs LogoutURL.
	LockLogsOut bool `json:"lockLogsOut"`
	// SSOLogoutDefaults are the events that log out at the provider when the user has not chosen.
	SSOLogoutDefaults SSOLogout `json:"ssoLogoutDefaults"`
}

// SSOLogout says which session events sign the browser out at the login provider.
type SSOLogout struct {
	OnLogout bool `json:"onLogout"`
	// OnLock only counts with Gateway.LockLogsOut.
	OnLock bool `json:"onLock"`
}

// Load reads, defaults and validates the config file.
func Load(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	return Parse(data)
}

// Parse defaults and validates config YAML. Unknown fields are errors.
func Parse(data []byte) (*Config, error) {
	c := &Config{}
	if err := yaml.UnmarshalStrict(data, c); err != nil {
		return nil, fmt.Errorf("parse config: %w", err)
	}
	c.Default()
	if err := c.validate(); err != nil {
		return nil, err
	}
	return c, nil
}

func ptr[T any](v T) *T { return &v }

func def[T comparable](v *T, d T) {
	var zero T
	if *v == zero {
		*v = d
	}
}

// Default fills unset fields with the documented defaults.
func (c *Config) Default() {
	if c.Namespace == "" {
		c.Namespace = os.Getenv("POD_NAMESPACE")
	}
	if c.Namespace == "" {
		if b, err := os.ReadFile("/var/run/secrets/kubernetes.io/serviceaccount/namespace"); err == nil {
			c.Namespace = strings.TrimSpace(string(b))
		}
	}
	def(&c.Names.Prefix, "desktop-")
	def(&c.MetricsAddr, ":9090")
	def(&c.HealthAddr, ":8081")

	if c.Controller.LeaderElection == nil {
		c.Controller.LeaderElection = ptr(true)
	}
	def(&c.Controller.LeaderElectionID, "kube-webtop-workspaces")
	if c.Controller.RequireAgentReady == nil {
		c.Controller.RequireAgentReady = ptr(true)
	}
	if c.Controller.CleanExitCodes == nil {
		c.Controller.CleanExitCodes = []int32{0}
	}
	def(&c.Controller.MaxConcurrentReconciles, 4)

	def(&c.PowerAPI.Addr, ":8082")
	def(&c.PowerAPI.Audience, "kube-webtop-workspaces")
	c.PowerAPI.URL = strings.TrimRight(c.PowerAPI.URL, "/")

	d := &c.Desktop
	def(&d.ImagePullPolicy, corev1.PullIfNotPresent)
	def(&d.Port, 8080)
	def(&d.ServiceAccountName, "webtop-desktop")
	if d.HostUsers == nil {
		d.HostUsers = ptr(false)
	}
	if d.ShmSize.IsZero() {
		d.ShmSize = resource.MustParse("1Gi")
	}
	if d.RunSizeLimit.IsZero() {
		d.RunSizeLimit = resource.MustParse("256Mi")
	}
	if d.TerminationGracePeriodSeconds == nil {
		d.TerminationGracePeriodSeconds = ptr(int64(420))
	}

	s := &c.Storage
	if s.Size.IsZero() {
		s.Size = resource.MustParse("50Gi")
	}
	if len(s.AccessModes) == 0 {
		s.AccessModes = []corev1.PersistentVolumeAccessMode{corev1.ReadWriteOnce}
	}
	def(&s.MountPath, "/persist")

	g := &c.Gateway
	def(&g.Addr, ":8080")
	def(&g.UserHeader, "Remote-User")
	def(&g.GroupsHeader, "Remote-Groups")
	def(&g.GroupsSeparator, ",")
	def(&g.DisplayNameHeader, "Remote-Name")
	if g.CookieSecure == nil {
		g.CookieSecure = ptr(true)
	}
}

var prefixRE = regexp.MustCompile(`^[a-z][a-z0-9-]*$`)

func (c *Config) validate() error {
	var errs []error
	if c.Namespace == "" {
		errs = append(errs, errors.New("namespace: not set and not running in a cluster"))
	}
	if len(c.Names.Prefix) > names.MaxPrefixLen || !prefixRE.MatchString(c.Names.Prefix) {
		errs = append(errs, fmt.Errorf("names.prefix %q: must match %s and be at most %d chars", c.Names.Prefix, prefixRE, names.MaxPrefixLen))
	}
	if c.Controller.MaxConcurrentReconciles < 1 {
		errs = append(errs, fmt.Errorf("controller.maxConcurrentReconciles %d: must be at least 1", c.Controller.MaxConcurrentReconciles))
	}
	if d := c.Desktop.StopAfterUnseen.Duration; d != 0 {
		if d < MinStopAfterUnseen {
			errs = append(errs, fmt.Errorf("desktop.stopAfterUnseen %s: must be 0 or at least %s", d, MinStopAfterUnseen))
		}
		if c.Controller.GatewayDeployment == "" {
			errs = append(errs, errors.New("controller.gatewayDeployment: required with desktop.stopAfterUnseen"))
		}
	}
	if u := c.Gateway.LogoutURL; u != "" {
		if p, err := url.Parse(u); err != nil || (p.Scheme != "http" && p.Scheme != "https") || p.Host == "" {
			errs = append(errs, errors.New("gateway.logoutUrl: must be an absolute http(s) URL"))
		}
	}
	if c.Gateway.LockLogsOut && c.Gateway.LogoutURL == "" {
		errs = append(errs, errors.New("gateway.lockLogsOut: requires gateway.logoutUrl"))
	}
	errs = append(errs, c.validateSelkies())
	return errors.Join(errs...)
}

// ValidateController checks the fields only the controller needs.
func (c *Config) ValidateController() error {
	var errs []error
	if c.Desktop.Image == "" {
		errs = append(errs, errors.New("desktop.image: required"))
	}
	if c.PowerAPI.URL == "" {
		errs = append(errs, errors.New("powerApi.url: required"))
	}
	return errors.Join(errs...)
}
