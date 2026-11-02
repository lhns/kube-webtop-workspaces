// SPDX-License-Identifier: Apache-2.0

package render

import (
	"maps"
	"slices"

	corev1 "k8s.io/api/core/v1"

	"github.com/lhns/kube-webtop-workspaces/api/v1alpha1"
	"github.com/lhns/kube-webtop-workspaces/internal/config"
)

// SelkiesEnvPrefix turns SELKIES_X into the pod variable WEBTOP_SELKIES_X, which webtop-init
// writes to /run/webtop/selkies.env (docs/contract.md, "Selkies settings").
const SelkiesEnvPrefix = "WEBTOP_"

// SelkiesEnv is desktop.selkies plus the rendered title as SELKIES_UI_TITLE, sorted by name so
// the template hash is stable. An explicit SELKIES_UI_TITLE in desktop.selkies wins over
// desktop.title. Values are single-line (config validates desktop.selkies, RenderTitle cleans
// the title).
func SelkiesEnv(cfg *config.Config, d *v1alpha1.Desktop) []corev1.EnvVar {
	m := maps.Clone(cfg.Desktop.Selkies)
	if m == nil {
		m = map[string]string{}
	}
	if _, explicit := m[config.KeySelkiesUITitle]; !explicit {
		// An execution error cannot happen after config validation; no title is the safe outcome.
		if t, ok, err := cfg.RenderTitle(config.TitleData{User: d.Spec.User, DisplayName: d.Spec.User, Desktop: d.Name}); ok && err == nil {
			m[config.KeySelkiesUITitle] = t
		}
	}
	env := make([]corev1.EnvVar, 0, len(m))
	for _, k := range slices.Sorted(maps.Keys(m)) {
		env = append(env, corev1.EnvVar{Name: SelkiesEnvPrefix + k, Value: m[k]})
	}
	return env
}
