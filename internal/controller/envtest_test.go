// SPDX-License-Identifier: Apache-2.0

//go:build !windows

package controller

import (
	"context"
	"errors"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/go-logr/logr"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/envtest"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"

	"github.com/lhns/kube-webtop-workspaces/api/v1alpha1"
	"github.com/lhns/kube-webtop-workspaces/internal/config"
	"github.com/lhns/kube-webtop-workspaces/internal/names"
)

const testNS = "default"

// fakeDir is a settable ldap.Directory.
type fakeDir struct {
	mu      sync.Mutex
	members map[string]struct{}
	err     error
}

func (f *fakeDir) Members(context.Context) (map[string]struct{}, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.err != nil {
		return nil, f.err
	}
	out := map[string]struct{}{}
	for k := range f.members {
		out[k] = struct{}{}
	}
	return out, nil
}

func (f *fakeDir) IsMember(_ context.Context, u string) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.err != nil {
		return false, f.err
	}
	_, ok := f.members[u]
	return ok, nil
}

func (f *fakeDir) setErr(err error) { f.mu.Lock(); f.err = err; f.mu.Unlock() }
func (f *fakeDir) remove(u string)  { f.mu.Lock(); delete(f.members, u); f.mu.Unlock() }

var suite struct {
	c    client.Client
	cfg  *config.Config
	dir  *fakeDir
	sync *LDAPSync
	rec  *Reconciler
}

func TestMain(m *testing.M) {
	if os.Getenv("KUBEBUILDER_ASSETS") == "" {
		os.Exit(m.Run()) // envtest cases skip themselves
	}
	ctrl.SetLogger(logr.Discard())
	env := &envtest.Environment{CRDDirectoryPaths: []string{"../../config/crd"}, ErrorIfCRDPathMissing: true}
	rest, err := env.Start()
	if err != nil {
		panic(err)
	}
	cfg, err := config.Parse([]byte(`
namespace: ` + testNS + `
desktop: {image: "img:1"}
powerApi: {url: "http://ctl.default.svc:8082"}
ldap: {url: "ldap://fake", baseDN: "dc=x", groupDN: "cn=g,dc=x"}
`))
	if err != nil {
		panic(err)
	}
	scheme, err := Scheme()
	if err != nil {
		panic(err)
	}
	mgr, err := ctrl.NewManager(rest, ctrl.Options{
		Scheme: scheme, Cache: CacheOptions(cfg), Metrics: metricsserver.Options{BindAddress: "0"},
	})
	if err != nil {
		panic(err)
	}
	dir := &fakeDir{members: map[string]struct{}{}}
	for _, u := range []string{"alice", "bob", "carol", "dave", "erin", "frank", "grace", "heidi"} {
		dir.members[u] = struct{}{}
	}
	rec, syncer, err := Setup(mgr, cfg, dir)
	if err != nil {
		panic(err)
	}
	direct, err := client.New(rest, client.Options{Scheme: scheme}) // uncached: the cache lags behind a Create
	if err != nil {
		panic(err)
	}
	suite.c, suite.cfg, suite.dir, suite.sync, suite.rec = direct, cfg, dir, syncer, rec

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		if err := mgr.Start(ctx); err != nil {
			panic(err)
		}
	}()
	code := m.Run()
	cancel()
	<-done
	_ = env.Stop()
	os.Exit(code)
}

func needEnv(t *testing.T) {
	t.Helper()
	if suite.c == nil {
		t.Skip("KUBEBUILDER_ASSETS not set")
	}
}

func eventually(t *testing.T, what string, f func() bool) {
	t.Helper()
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		if f() {
			return
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

func consistently(t *testing.T, what string, d time.Duration, f func() bool) {
	t.Helper()
	end := time.Now().Add(d)
	for time.Now().Before(end) {
		if !f() {
			t.Fatalf("%s did not hold", what)
		}
		time.Sleep(100 * time.Millisecond)
	}
}

func key(name string) client.ObjectKey { return client.ObjectKey{Namespace: testNS, Name: name} }

func getDesktop(t *testing.T, name string) *v1alpha1.Desktop {
	t.Helper()
	d := &v1alpha1.Desktop{}
	if err := suite.c.Get(context.Background(), key(name), d); err != nil {
		t.Fatal(err)
	}
	return d
}

func tryDeployment(name string) *appsv1.Deployment {
	dep := &appsv1.Deployment{}
	if err := suite.c.Get(context.Background(), key("desktop-"+name), dep); err != nil {
		return nil
	}
	return dep
}

func replicas(dep *appsv1.Deployment) int32 {
	if dep == nil || dep.Spec.Replicas == nil {
		return -1
	}
	return *dep.Spec.Replicas
}

func createDesktop(t *testing.T, name string, on bool) *v1alpha1.Desktop {
	t.Helper()
	d := &v1alpha1.Desktop{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: testNS},
		Spec:       v1alpha1.DesktopSpec{User: name, PowerState: v1alpha1.PowerOff},
	}
	if on {
		d.Spec.PowerState = v1alpha1.PowerOn
	}
	if err := suite.c.Create(context.Background(), d); err != nil {
		t.Fatal(err)
	}
	return d
}

func patchSpec(t *testing.T, name string, mod func(*v1alpha1.Desktop)) {
	t.Helper()
	d := getDesktop(t, name)
	orig := d.DeepCopy()
	mod(d)
	if err := suite.c.Patch(context.Background(), d, client.MergeFrom(orig)); err != nil {
		t.Fatal(err)
	}
}

func poke(t *testing.T, name string) {
	patchSpec(t, name, func(d *v1alpha1.Desktop) {
		if d.Annotations == nil {
			d.Annotations = map[string]string{}
		}
		d.Annotations["test/poke"] = time.Now().String()
	})
}

func waitRunning(t *testing.T, name string) {
	t.Helper()
	eventually(t, name+" deployment at 1 with lastStart", func() bool {
		d := getDesktop(t, name)
		return replicas(tryDeployment(name)) == 1 && d.Status.LastStart != nil && d.Status.AppliedTemplateHash != ""
	})
}

func pvcExists(t *testing.T, name string) *corev1.PersistentVolumeClaim {
	t.Helper()
	pvc := &corev1.PersistentVolumeClaim{}
	if err := suite.c.Get(context.Background(), key("desktop-"+name), pvc); err != nil {
		t.Fatalf("PVC of %s: %v", name, err)
	}
	return pvc
}

func TestStartCreatesObjects(t *testing.T) {
	needEnv(t)
	d := createDesktop(t, "alice", true)
	waitRunning(t, "alice")
	d = getDesktop(t, "alice")

	dep := tryDeployment("alice")
	if !metav1.IsControlledBy(dep, d) {
		t.Fatal("deployment not controlled by the Desktop")
	}
	svc := &corev1.Service{}
	if err := suite.c.Get(context.Background(), key("desktop-alice"), svc); err != nil || !metav1.IsControlledBy(svc, d) {
		t.Fatalf("service: %v", err)
	}
	pvc := pvcExists(t, "alice")
	if len(pvc.OwnerReferences) != 0 {
		t.Fatal("PVC must have no ownerReferences")
	}
	if pvc.Labels[names.LabelDesktop] != "alice" {
		t.Fatalf("pvc labels: %v", pvc.Labels)
	}
	if d.Status.URL != "http://desktop-alice.default.svc:8080" {
		t.Fatalf("url: %s", d.Status.URL)
	}
	eventually(t, "Starting/Scheduling status", func() bool {
		d := getDesktop(t, "alice")
		return d.Status.Phase == v1alpha1.PhaseStarting && d.Status.Stage == "Scheduling"
	})
	eventually(t, "Authorized condition", func() bool {
		for _, c := range getDesktop(t, "alice").Status.Conditions {
			if c.Type == v1alpha1.ConditionAuthorized && c.Status == metav1.ConditionTrue {
				return true
			}
		}
		return false
	})

	// Deleting the Desktop leaves the PVC alone (garbage collection is not part of envtest).
	if err := suite.c.Delete(context.Background(), d); err != nil {
		t.Fatal(err)
	}
	eventually(t, "desktop gone", func() bool {
		return apierrors.IsNotFound(suite.c.Get(context.Background(), key("alice"), &v1alpha1.Desktop{}))
	})
	consistently(t, "PVC untouched", 2*time.Second, func() bool {
		pvc := pvcExists(t, "alice")
		return pvc.DeletionTimestamp.IsZero() && len(pvc.OwnerReferences) == 0
	})
}

func TestLDAPRemovalStopsDesktop(t *testing.T) {
	needEnv(t)
	createDesktop(t, "bob", true)
	waitRunning(t, "bob")

	suite.dir.remove("bob")
	if err := suite.sync.SyncOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	eventually(t, "bob stopped by ldap", func() bool {
		d := getDesktop(t, "bob")
		return d.Spec.PowerState == v1alpha1.PowerOff && d.Status.LastStop != nil &&
			d.Status.LastStop.Reason == v1alpha1.StopLDAP && replicas(tryDeployment("bob")) == 0
	})
	eventually(t, "Authorized=False", func() bool {
		for _, c := range getDesktop(t, "bob").Status.Conditions {
			if c.Type == v1alpha1.ConditionAuthorized && c.Status == metav1.ConditionFalse && c.Reason == v1alpha1.ReasonNotMember {
				return true
			}
		}
		return false
	})
	pvc := pvcExists(t, "bob")
	if !pvc.DeletionTimestamp.IsZero() {
		t.Fatal("PVC must stay")
	}
	// Being re-added does not start it.
	suite.dir.mu.Lock()
	suite.dir.members["bob"] = struct{}{}
	suite.dir.mu.Unlock()
	if err := suite.sync.SyncOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	consistently(t, "bob stays off", time.Second, func() bool {
		return getDesktop(t, "bob").Spec.PowerState == v1alpha1.PowerOff && replicas(tryDeployment("bob")) == 0
	})
}

func TestLDAPErrorChangesNothing(t *testing.T) {
	needEnv(t)
	createDesktop(t, "carol", true)
	waitRunning(t, "carol")

	suite.dir.setErr(errors.New("ldap down"))
	defer suite.dir.setErr(nil)
	if err := suite.sync.SyncOnce(context.Background()); err == nil {
		t.Fatal("sync must report the error")
	}
	// An empty group is an error too and removes nobody.
	suite.dir.setErr(nil)
	saved := suite.dir.members
	suite.dir.mu.Lock()
	suite.dir.members = map[string]struct{}{}
	suite.dir.mu.Unlock()
	if err := suite.sync.SyncOnce(context.Background()); err == nil {
		t.Fatal("empty group must be an error")
	}
	suite.dir.mu.Lock()
	suite.dir.members = saved
	suite.dir.mu.Unlock()

	consistently(t, "carol keeps running", 2*time.Second, func() bool {
		return getDesktop(t, "carol").Spec.PowerState == v1alpha1.PowerOn && replicas(tryDeployment("carol")) == 1
	})
}

// setImage rolls out a new desktop image the way a controller restart with new config would.
func setImage(img string) {
	c := *suite.rec.Config()
	c.Desktop.Image = img
	suite.rec.SetConfig(&c)
}

func TestUpdatePendingUntilRestart(t *testing.T) {
	needEnv(t)
	defer setImage("img:1")
	createDesktop(t, "dave", true)
	waitRunning(t, "dave")
	image := func() string { return tryDeployment("dave").Spec.Template.Spec.Containers[0].Image }
	if image() != "img:1" {
		t.Fatal(image())
	}
	hash1 := getDesktop(t, "dave").Status.AppliedTemplateHash

	setImage("img:2")
	poke(t, "dave")
	eventually(t, "updatePending", func() bool { return getDesktop(t, "dave").Status.UpdatePending })
	if image() != "img:1" || replicas(tryDeployment("dave")) != 1 {
		t.Fatal("a running desktop's template must not change")
	}
	if getDesktop(t, "dave").Status.AppliedTemplateHash != hash1 {
		t.Fatal("applied hash must stay")
	}

	patchSpec(t, "dave", func(d *v1alpha1.Desktop) {
		now := metav1.Now()
		d.Spec.RestartRequestedAt = &now
	})
	eventually(t, "template applied by restart", func() bool {
		d := getDesktop(t, "dave")
		return image() == "img:2" && !d.Status.UpdatePending && d.Status.ObservedRestartAt != nil &&
			d.Status.AppliedTemplateHash != hash1
	})
	if _, ok := tryDeployment("dave").Spec.Template.Annotations[names.AnnotationRestartedAt]; !ok {
		t.Fatal("restarted-at not stamped")
	}

	// Off: the template is applied at replicas 0.
	patchSpec(t, "dave", func(d *v1alpha1.Desktop) { d.Spec.PowerState = v1alpha1.PowerOff })
	eventually(t, "scaled to 0", func() bool { return replicas(tryDeployment("dave")) == 0 })
	setImage("img:3")
	poke(t, "dave")
	eventually(t, "template applied while off", func() bool { return image() == "img:3" })
	if replicas(tryDeployment("dave")) != 0 {
		t.Fatal("must stay at 0")
	}
	eventually(t, "admin stop recorded", func() bool {
		ls := getDesktop(t, "dave").Status.LastStop
		return ls != nil && ls.Reason == v1alpha1.StopAdmin
	})
}

func TestForeignPVCBlocksStart(t *testing.T) {
	needEnv(t)
	pvc := &corev1.PersistentVolumeClaim{
		ObjectMeta: metav1.ObjectMeta{Name: "desktop-erin", Namespace: testNS},
		Spec: corev1.PersistentVolumeClaimSpec{
			AccessModes: []corev1.PersistentVolumeAccessMode{corev1.ReadWriteOnce},
			Resources: corev1.VolumeResourceRequirements{
				Requests: corev1.ResourceList{corev1.ResourceStorage: resource.MustParse("1Gi")},
			},
		},
	}
	if err := suite.c.Create(context.Background(), pvc); err != nil {
		t.Fatal(err)
	}
	createDesktop(t, "erin", true)
	eventually(t, "phase Error", func() bool {
		d := getDesktop(t, "erin")
		return d.Status.Phase == v1alpha1.PhaseError && d.Status.Message != ""
	})
	consistently(t, "nothing starts", 2*time.Second, func() bool {
		dep := tryDeployment("erin")
		return replicas(dep) <= 0 && getDesktop(t, "erin").Status.LastStart == nil
	})
	if got := pvcExists(t, "erin"); len(got.Labels) != 0 || len(got.Annotations) != 0 {
		t.Fatalf("foreign PVC modified: %v %v", got.Labels, got.Annotations)
	}

	// Labeling it adopts it, adding only the missing annotation.
	got := pvcExists(t, "erin")
	orig := got.DeepCopy()
	got.Labels = map[string]string{names.LabelDesktop: "erin"}
	if err := suite.c.Patch(context.Background(), got, client.MergeFrom(orig)); err != nil {
		t.Fatal(err)
	}
	waitRunning(t, "erin")
	adopted := pvcExists(t, "erin")
	if adopted.Annotations[names.AnnotationUser] != "erin" || len(adopted.OwnerReferences) != 0 {
		t.Fatalf("adopted PVC: %v %v", adopted.Annotations, adopted.OwnerReferences)
	}
}

func TestCleanExitStopsDesktop(t *testing.T) {
	needEnv(t)
	createDesktop(t, "frank", true)
	waitRunning(t, "frank")
	d := getDesktop(t, "frank")
	started := d.Status.LastStart.Time

	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: "desktop-frank-x", Namespace: testNS, Labels: names.Labels("frank")},
		Spec:       corev1.PodSpec{Containers: []corev1.Container{{Name: "desktop", Image: "img:1"}}},
	}
	if err := suite.c.Create(context.Background(), pod); err != nil {
		t.Fatal(err)
	}
	pod.Status.ContainerStatuses = []corev1.ContainerStatus{{
		Name: "desktop", Image: "img:1", ImageID: "img:1",
		State: corev1.ContainerState{Terminated: &corev1.ContainerStateTerminated{
			ExitCode: 0, StartedAt: metav1.NewTime(started), FinishedAt: metav1.NewTime(started.Add(10 * time.Second)),
		}},
	}}
	if err := suite.c.Status().Update(context.Background(), pod); err != nil {
		t.Fatal(err)
	}
	eventually(t, "stopped by exit", func() bool {
		d := getDesktop(t, "frank")
		return d.Spec.PowerState == v1alpha1.PowerOff && d.Status.LastStop != nil &&
			d.Status.LastStop.Reason == v1alpha1.StopExit && replicas(tryDeployment("frank")) == 0
	})
}

func TestInvalidNameIsError(t *testing.T) {
	needEnv(t)
	d := &v1alpha1.Desktop{
		ObjectMeta: metav1.ObjectMeta{Name: "wrong", Namespace: testNS},
		Spec:       v1alpha1.DesktopSpec{User: "grace", PowerState: v1alpha1.PowerOn},
	}
	if err := suite.c.Create(context.Background(), d); err != nil {
		t.Fatal(err)
	}
	eventually(t, "phase Error", func() bool { return getDesktop(t, "wrong").Status.Phase == v1alpha1.PhaseError })
	consistently(t, "nothing created", time.Second, func() bool {
		return tryDeployment("wrong") == nil
	})
}

// Kubelet events of the pod refine the stage while the container waits, and a completed pull
// is recorded for the download estimate.
func TestPodEventsRefineStage(t *testing.T) {
	needEnv(t)
	createDesktop(t, "heidi", true)
	waitRunning(t, "heidi")
	ctx := context.Background()

	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: "desktop-heidi-x", Namespace: testNS, Labels: names.Labels("heidi")},
		Spec:       corev1.PodSpec{Containers: []corev1.Container{{Name: "desktop", Image: "img:1"}}},
	}
	if err := suite.c.Create(ctx, pod); err != nil {
		t.Fatal(err)
	}
	pod.Status.ContainerStatuses = []corev1.ContainerStatus{{
		Name: "desktop", Image: "img:1", ImageID: "",
		State: corev1.ContainerState{Waiting: &corev1.ContainerStateWaiting{Reason: "ContainerCreating"}},
	}}
	if err := suite.c.Status().Update(ctx, pod); err != nil {
		t.Fatal(err)
	}
	mkEvent := func(name, reason, msg string, at time.Time) {
		t.Helper()
		e := &corev1.Event{
			ObjectMeta:     metav1.ObjectMeta{Name: name, Namespace: testNS},
			InvolvedObject: corev1.ObjectReference{Kind: "Pod", Namespace: testNS, Name: pod.Name, UID: pod.UID},
			Reason:         reason, Message: msg, Type: corev1.EventTypeNormal,
			FirstTimestamp: metav1.NewTime(at), LastTimestamp: metav1.NewTime(at),
		}
		if err := suite.c.Create(ctx, e); err != nil {
			t.Fatal(err)
		}
	}
	stage := func() string { return getDesktop(t, "heidi").Status.Stage }
	now := time.Now()

	eventually(t, "CreatingContainer without events", func() bool { return stage() == "CreatingContainer" })
	mkEvent("heidi-attach", "FailedAttachVolume", "AttachVolume.Attach failed", now.Add(-30*time.Second))
	eventually(t, "AttachingVolume", func() bool { return stage() == "AttachingVolume" })
	mkEvent("heidi-attached", "SuccessfulAttachVolume", "attached", now.Add(-20*time.Second))
	mkEvent("heidi-pulling", "Pulling", `Pulling image "img:1"`, now.Add(-10*time.Second))
	eventually(t, "PullingImage with startedAt", func() bool {
		ip := getDesktop(t, "heidi").Status.ImagePull
		return stage() == "PullingImage" && ip != nil && ip.StartedAt != nil && ip.Image == "img:1"
	})
	mkEvent("heidi-pulled", "Pulled", `Successfully pulled image "img:1" in 9m13.252s (9m13.252s including waiting). Image size: 3745009478 bytes.`, now)
	eventually(t, "pull recorded, stage back to CreatingContainer", func() bool {
		ip := getDesktop(t, "heidi").Status.ImagePull
		return stage() == "CreatingContainer" && ip != nil && ip.StartedAt == nil &&
			ip.LastDurationSeconds == 553 && ip.LastSizeBytes == 3745009478
	})
}
