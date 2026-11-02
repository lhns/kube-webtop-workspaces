// SPDX-License-Identifier: Apache-2.0

package render

import (
	"maps"
	"slices"
	"strings"
	"testing"
	"unicode"
	"unicode/utf8"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/lhns/kube-webtop-workspaces/api/v1alpha1"
	"github.com/lhns/kube-webtop-workspaces/internal/config"
	"github.com/lhns/kube-webtop-workspaces/internal/names"
)

func testConfig(t *testing.T) *config.Config {
	t.Helper()
	cfg, err := config.Parse([]byte("namespace: webtop\ndesktop:\n  image: img:1\npowerApi:\n  url: http://ctl.webtop.svc:8082/\n"))
	if err != nil {
		t.Fatal(err)
	}
	return cfg
}

func testDesktop() *v1alpha1.Desktop {
	return &v1alpha1.Desktop{
		ObjectMeta: metav1.ObjectMeta{Name: "alice", Namespace: "webtop", UID: "u1"},
		Spec:       v1alpha1.DesktopSpec{User: "Alice"},
	}
}

func TestHashIgnoresRestartedAt(t *testing.T) {
	cfg, d := testConfig(t), testDesktop()
	a := PodTemplate(cfg, d)
	b := PodTemplate(cfg, d)
	b.Annotations[names.AnnotationRestartedAt] = "2026-01-01T00:00:00Z"
	if TemplateHash(a) != TemplateHash(b) {
		t.Fatal("restarted-at must not change the hash")
	}
	if _, ok := b.Annotations[names.AnnotationRestartedAt]; !ok {
		t.Fatal("TemplateHash must not mutate its input")
	}
}

func TestHashChangesOnImage(t *testing.T) {
	cfg, d := testConfig(t), testDesktop()
	h1 := TemplateHash(PodTemplate(cfg, d))
	cfg.Desktop.Image = "img:2"
	if h1 == TemplateHash(PodTemplate(cfg, d)) {
		t.Fatal("image change must change the hash")
	}
}

func TestPVC(t *testing.T) {
	cfg, d := testConfig(t), testDesktop()
	cfg.Storage.Labels = map[string]string{"extra": "l", names.LabelDesktop: "evil"}
	cfg.Storage.Annotations = map[string]string{"extra": "a", names.AnnotationUser: "evil"}
	pvc := PVC(cfg, d)
	if len(pvc.OwnerReferences) != 0 {
		t.Fatal("PVC must have no ownerReferences")
	}
	if pvc.Name != "desktop-alice" || pvc.Namespace != "webtop" {
		t.Fatalf("name/ns: %s/%s", pvc.Namespace, pvc.Name)
	}
	if pvc.Labels[names.LabelDesktop] != "alice" || pvc.Labels["extra"] != "l" {
		t.Fatalf("labels: %v", pvc.Labels)
	}
	if pvc.Annotations[names.AnnotationUser] != "Alice" || pvc.Annotations["extra"] != "a" {
		t.Fatalf("annotations: %v", pvc.Annotations)
	}
	if pvc.Spec.StorageClassName != nil {
		t.Fatal("storage class must be unset when not configured")
	}
	cfg.Storage.StorageClassName = "fast"
	if sc := PVC(cfg, d).Spec.StorageClassName; sc == nil || *sc != "fast" {
		t.Fatal("storage class not set")
	}
}

func TestPodTemplate(t *testing.T) {
	cfg, d := testConfig(t), testDesktop()
	cfg.Desktop.ExtraEnv = []corev1.EnvVar{{Name: "X", Value: "y"}}
	cfg.Desktop.PodLabels = map[string]string{"team": "a", names.LabelDesktop: "evil"}
	tpl := PodTemplate(cfg, d)
	if tpl.Labels[names.LabelDesktop] != "alice" || tpl.Labels["team"] != "a" {
		t.Fatalf("labels: %v", tpl.Labels)
	}
	p := tpl.Spec
	if p.HostUsers == nil || *p.HostUsers || *p.AutomountServiceAccountToken || *p.EnableServiceLinks {
		t.Fatal("pod security fields")
	}
	if len(p.Containers) != 1 || p.Containers[0].Name != "desktop" {
		t.Fatal("container")
	}
	c := p.Containers[0]
	env := map[string]string{}
	for _, e := range c.Env {
		env[e.Name] = e.Value
	}
	want := map[string]string{
		"WEBTOP_CONTROLLER_URL": "http://ctl.webtop.svc:8082", "WEBTOP_TOKEN_PATH": "/run/webtop/sa/token",
		"WEBTOP_DESKTOP": "alice", "WEBTOP_USER": "Alice", "WEBTOP_NAMESPACE": "webtop",
		"WEBTOP_HTTP_PORT": "8080", "WEBTOP_PERSIST_PATH": "/persist", "X": "y",
	}
	for k, v := range want {
		if env[k] != v {
			t.Errorf("env %s = %q, want %q", k, env[k], v)
		}
	}
	if len(c.Ports) != 2 || c.Ports[1].Name != "agent" || c.Ports[1].ContainerPort != names.AgentPort {
		t.Errorf("container ports: %+v", c.Ports)
	}
	if svc := Service(cfg, d).Spec.Ports; len(svc) != 2 || svc[1].Name != "agent" || svc[1].Port != names.AgentPort {
		t.Errorf("service ports: %+v", svc)
	}
	vols := map[string]corev1.Volume{}
	for _, v := range p.Volumes {
		vols[v.Name] = v
	}
	if len(vols) != 4 {
		t.Fatalf("volumes: %d", len(vols))
	}
	sa := vols[volSA].Projected.Sources[0].ServiceAccountToken
	if sa.Audience != cfg.PowerAPI.Audience || *sa.ExpirationSeconds != 3600 || sa.Path != "token" {
		t.Fatalf("token projection: %+v", sa)
	}
	if vols[volData].PersistentVolumeClaim.ClaimName != "desktop-alice" {
		t.Fatal("pvc claim")
	}
	if c.SecurityContext == nil || *c.SecurityContext.RunAsUser != 0 || c.ReadinessProbe.TCPSocket == nil {
		t.Fatal("security context / probe")
	}
	cfg.Desktop.SecurityContext = &corev1.SecurityContext{RunAsUser: new(int64)}
	if PodTemplate(cfg, d).Spec.Containers[0].SecurityContext.Capabilities != nil {
		t.Fatal("securityContext override must replace the default")
	}
}

func TestDeployment(t *testing.T) {
	cfg, d := testConfig(t), testDesktop()
	dep := Deployment(cfg, d, 1)
	if dep.Spec.Strategy.Type != "Recreate" || *dep.Spec.Replicas != 1 {
		t.Fatal("strategy/replicas")
	}
	if dep.Annotations[names.AnnotationTemplateHash] != TemplateHash(dep.Spec.Template) {
		t.Fatal("hash annotation")
	}
	if got := ServiceURL(cfg, d); got != "http://desktop-alice.webtop.svc:8080" {
		t.Fatal(got)
	}
}

func selkiesEnvOf(t *testing.T, cfg *config.Config, d *v1alpha1.Desktop) map[string]string {
	t.Helper()
	got := map[string]string{}
	for _, e := range PodTemplate(cfg, d).Spec.Containers[0].Env {
		if strings.HasPrefix(e.Name, "WEBTOP_SELKIES_") {
			got[strings.TrimPrefix(e.Name, "WEBTOP_")] = e.Value
		}
	}
	return got
}

func parse(t *testing.T, desktop string) *config.Config {
	t.Helper()
	cfg, err := config.Parse([]byte("namespace: webtop\npowerApi:\n  url: http://ctl:8082\ndesktop:\n  image: img:1\n" + desktop))
	if err != nil {
		t.Fatal(err)
	}
	return cfg
}

func TestSelkiesNothingConfigured(t *testing.T) {
	if got := selkiesEnvOf(t, testConfig(t), testDesktop()); len(got) != 0 {
		t.Fatalf("no title and no settings must add no env, got %v", got)
	}
}

func TestSelkiesTitleAndSettings(t *testing.T) {
	cfg := parse(t, "  title: '{{ .DisplayName }} ({{ .Desktop }})'\n  selkies:\n    SELKIES_FRAMERATE: \"60\"\n")
	got := selkiesEnvOf(t, cfg, testDesktop())
	want := map[string]string{"SELKIES_UI_TITLE": "Alice (alice)", "SELKIES_FRAMERATE": "60"}
	if !maps.Equal(got, want) {
		t.Fatalf("got %v, want %v", got, want)
	}
}

func TestSelkiesExplicitTitleWinsOverTemplate(t *testing.T) {
	cfg := parse(t, "  title: '{{ .User }}'\n  selkies:\n    SELKIES_UI_TITLE: '{{ .User }} literal'\n")
	if got := selkiesEnvOf(t, cfg, testDesktop())["SELKIES_UI_TITLE"]; got != "{{ .User }} literal" {
		t.Fatalf("explicit setting must win and not be templated, got %q", got)
	}
}

// User text is template data, never template source.
func TestSelkiesTitleDataIsNotTemplate(t *testing.T) {
	cfg := parse(t, "  title: '{{ .User }}'\n")
	d := testDesktop()
	d.Spec.User = "{{ .Desktop }}<b>x</b>"
	if got := selkiesEnvOf(t, cfg, d)["SELKIES_UI_TITLE"]; got != "{{ .Desktop }}<b>x</b>" {
		t.Fatalf("data was interpreted: %q", got)
	}
}

func TestSelkiesTitleStripsControlCharacters(t *testing.T) {
	cfg := parse(t, "  title: '{{ .User }}'\n")
	d := testDesktop()
	d.Spec.User = "a\r\nSELKIES_EVIL=1\x00\x1b[0m b\u0085c\x7f"
	got := selkiesEnvOf(t, cfg, d)["SELKIES_UI_TITLE"]
	for _, r := range got {
		if r < ' ' || unicode.IsControl(r) || r == ' ' {
			t.Fatalf("control character %q left in %q", r, got)
		}
	}
	if got != "a  SELKIES_EVIL=1  [0m b c" {
		t.Fatalf("got %q", got)
	}
}

func TestSelkiesTitleCappedAndValidUTF8(t *testing.T) {
	cfg := parse(t, "  title: '{{ .User }}'\n")
	d := testDesktop()
	d.Spec.User = strings.Repeat("é", 1000) + "\xff"
	got := selkiesEnvOf(t, cfg, d)["SELKIES_UI_TITLE"]
	if n := utf8.RuneCountInString(got); n != config.MaxSelkiesValueLen || !utf8.ValidString(got) {
		t.Fatalf("got %d runes, valid=%v", n, utf8.ValidString(got))
	}
	d.Spec.User = "\xff"
	if got := selkiesEnvOf(t, cfg, d)["SELKIES_UI_TITLE"]; got != "�" {
		t.Fatalf("got %q", got)
	}
}

func TestSelkiesEmptyTitleIsOmitted(t *testing.T) {
	cfg := parse(t, "  title: '{{ \"\" }}'\n")
	if got := selkiesEnvOf(t, cfg, testDesktop()); len(got) != 0 {
		t.Fatalf("got %v", got)
	}
}

func TestSelkiesDeterministicAndHashed(t *testing.T) {
	cfg := parse(t, "  title: '{{ .User }}'\n  selkies:\n    SELKIES_B: b\n    SELKIES_A: a\n    SELKIES_C: c\n")
	d := testDesktop()
	var names []string
	for _, e := range PodTemplate(cfg, d).Spec.Containers[0].Env {
		if strings.HasPrefix(e.Name, "WEBTOP_SELKIES_") {
			names = append(names, e.Name)
		}
	}
	if !slices.IsSorted(names) || len(names) != 4 {
		t.Fatalf("not sorted: %v", names)
	}
	h := TemplateHash(PodTemplate(cfg, d))
	for range 20 {
		if TemplateHash(PodTemplate(cfg, d)) != h {
			t.Fatal("hash is not deterministic")
		}
	}
	cfg.Desktop.Selkies["SELKIES_A"] = "other"
	if TemplateHash(PodTemplate(cfg, d)) == h {
		t.Fatal("a setting change must change the hash")
	}
}
