// SPDX-License-Identifier: Apache-2.0

package render

import (
	"testing"

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
	cfg.Storage.Annotations = map[string]string{"extra": "a"}
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
