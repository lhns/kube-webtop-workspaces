// SPDX-License-Identifier: Apache-2.0

package controller

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/equality"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	crcontroller "sigs.k8s.io/controller-runtime/pkg/controller"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
	"sigs.k8s.io/controller-runtime/pkg/source"

	"github.com/lhns/kube-webtop-workspaces/api/v1alpha1"
	"github.com/lhns/kube-webtop-workspaces/internal/config"
	"github.com/lhns/kube-webtop-workspaces/internal/ldap"
	"github.com/lhns/kube-webtop-workspaces/internal/lifecycle"
	"github.com/lhns/kube-webtop-workspaces/internal/metrics"
	"github.com/lhns/kube-webtop-workspaces/internal/names"
	"github.com/lhns/kube-webtop-workspaces/internal/render"
)

// +kubebuilder:rbac:groups=webtop.lhns.github.io,resources=desktops,verbs=get;list;watch;update;patch
// +kubebuilder:rbac:groups=webtop.lhns.github.io,resources=desktops/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=apps,resources=deployments,verbs=get;list;watch;create;update;patch
// +kubebuilder:rbac:groups="",resources=services,verbs=get;list;watch;create;update;patch
// +kubebuilder:rbac:groups="",resources=persistentvolumeclaims,verbs=get;list;watch;create;patch
// +kubebuilder:rbac:groups="",resources=pods,verbs=get;list;watch
// +kubebuilder:rbac:groups="",resources=events,verbs=create;patch
// +kubebuilder:rbac:groups=coordination.k8s.io,resources=leases,verbs=get;list;watch;create;update;patch

// Reconciler reconciles Desktops (docs/contract.md, section 3.2).
type Reconciler struct {
	client.Client
	// APIReader reads the Deployment uncached: the replicas it holds decide whether a template
	// may be written, so a stale cache entry must not be trusted.
	APIReader client.Reader
	cfg       atomic.Pointer[config.Config]
	// Dir is nil when LDAP is disabled.
	Dir     ldap.Directory
	Members *MemberCache
	Now     func() time.Time
	// Notify delivers events that make the reconciler look at a Desktop again (LDAP sync).
	Notify chan event.GenericEvent
	// startObserved is the lastStart (per desktop) whose start duration was observed, so a
	// reconcile on a cache that has not seen our Running write yet does not observe it again.
	startObserved sync.Map
}

// Config returns the config in use. SetConfig swaps it atomically (a reconcile keeps the one it
// started with), so tests can roll out a new desktop template without a data race.
func (r *Reconciler) Config() *config.Config { return r.cfg.Load() }

func (r *Reconciler) SetConfig(c *config.Config) { r.cfg.Store(c) }

func (r *Reconciler) now() time.Time {
	if r.Now != nil {
		return r.Now()
	}
	return time.Now()
}

// SetupWithManager wires the watches: Desktops, owned Deployments and Services, desktop pods.
func (r *Reconciler) SetupWithManager(mgr ctrl.Manager) error {
	podToDesktop := handler.EnqueueRequestsFromMapFunc(func(_ context.Context, o client.Object) []reconcile.Request {
		d := o.GetLabels()[names.LabelDesktop]
		if d == "" {
			return nil
		}
		return []reconcile.Request{{NamespacedName: types.NamespacedName{Namespace: o.GetNamespace(), Name: d}}}
	})
	b := ctrl.NewControllerManagedBy(mgr).
		Named("desktop").
		For(&v1alpha1.Desktop{}).
		Owns(&appsv1.Deployment{}).
		Owns(&corev1.Service{}).
		Watches(&corev1.Pod{}, podToDesktop).
		WithOptions(crcontroller.Options{MaxConcurrentReconciles: r.Config().Controller.MaxConcurrentReconciles})
	if r.Notify != nil {
		b = b.WatchesRawSource(source.Channel(r.Notify, &handler.EnqueueRequestForObject{}))
	}
	return b.Complete(r)
}

// authResult is the outcome of the authorization step.
type authResult struct {
	Status  metav1.ConditionStatus
	Reason  string
	Message string
	// AllowStart is true when a new start may go ahead (an LDAP error follows errorPolicy).
	AllowStart bool
}

func (r *Reconciler) authorize(ctx context.Context, d *v1alpha1.Desktop, starting bool) authResult {
	if !r.Config().LDAP.Enabled() || r.Dir == nil {
		return authResult{Status: metav1.ConditionTrue, Reason: v1alpha1.ReasonLDAPDisabled, AllowStart: true}
	}
	member, known := r.Members.Contains(d.Spec.User)
	if member {
		return authResult{Status: metav1.ConditionTrue, Reason: v1alpha1.ReasonMember, AllowStart: true}
	}
	if known && !starting {
		return notMember()
	}
	if !known && !starting {
		return r.ldapError("no successful LDAP lookup yet")
	}
	// A start of a user the cache does not list: ask the directory. This blocks this worker for up
	// to ldap.timeout; controller.maxConcurrentReconciles keeps other desktops reconciling meanwhile.
	ok, err := r.Dir.IsMember(ctx, d.Spec.User)
	if err != nil {
		slog.Warn("ldap lookup failed", "desktop", d.Name, "err", err)
		return r.ldapError("LDAP lookup failed")
	}
	if !ok {
		return notMember()
	}
	r.Members.Add(d.Spec.User)
	return authResult{Status: metav1.ConditionTrue, Reason: v1alpha1.ReasonMember, AllowStart: true}
}

func notMember() authResult {
	return authResult{Status: metav1.ConditionFalse, Reason: v1alpha1.ReasonNotMember, Message: "user is not a member of the desktop group"}
}

func (r *Reconciler) ldapError(msg string) authResult {
	return authResult{Status: metav1.ConditionUnknown, Reason: v1alpha1.ReasonLDAPError, Message: msg,
		AllowStart: r.Config().LDAP.ErrorPolicy == config.ErrorPolicyAllow}
}

// patchStatus writes the status fields that changed since base, then moves base forward.
func (r *Reconciler) patchStatus(ctx context.Context, d, base *v1alpha1.Desktop) error {
	if equality.Semantic.DeepEqual(d.Status, base.Status) {
		return nil
	}
	if err := r.Status().Patch(ctx, d, client.MergeFrom(base)); err != nil {
		return err
	}
	*base = *d.DeepCopy()
	return nil
}

func (r *Reconciler) Reconcile(ctx context.Context, req reconcile.Request) (reconcile.Result, error) {
	d := &v1alpha1.Desktop{}
	if err := r.Get(ctx, req.NamespacedName, d); err != nil {
		if apierrors.IsNotFound(err) {
			r.startObserved.Delete(req.Name)
		}
		return reconcile.Result{}, client.IgnoreNotFound(err)
	}
	if !d.DeletionTimestamp.IsZero() {
		return reconcile.Result{}, nil
	}
	base := d.DeepCopy()
	cfg := r.Config()

	var pods corev1.PodList
	if err := r.List(ctx, &pods, client.InNamespace(d.Namespace), client.MatchingLabels{names.LabelDesktop: d.Name}); err != nil {
		return reconcile.Result{}, err
	}
	pod := newestPod(pods.Items)

	// 1. Validate.
	var errMsg string
	if want, err := names.Sanitize(d.Spec.User); err != nil || want != d.Name {
		errMsg = fmt.Sprintf("name %q is not the sanitized user ID %q", d.Name, want)
		if err != nil {
			errMsg = "user ID has no usable characters"
		}
		return reconcile.Result{}, r.finish(ctx, d, base, pods.Items, nil, finishArgs{
			errMsg: errMsg, auth: authResult{Status: metav1.ConditionUnknown, Reason: v1alpha1.ReasonInvalidName, Message: errMsg},
		})
	}

	// The Deployment, read uncached.
	var cur int32
	dep := &appsv1.Deployment{}
	depExists := true
	if err := r.APIReader.Get(ctx, client.ObjectKey{Namespace: d.Namespace, Name: render.ObjectName(cfg, d)}, dep); err != nil {
		if !apierrors.IsNotFound(err) {
			return reconcile.Result{}, err
		}
		depExists = false
	} else {
		cur = replicasOf(dep)
	}

	// 7. Clean exit of a running desktop.
	on := d.Spec.PowerState == v1alpha1.PowerOn
	if on && cur > 0 && cleanExit(d, pod, cfg.Controller.CleanExitCodes) {
		if err := lifecycle.Stop(ctx, r.Client, d, v1alpha1.StopExit, r.now()); err != nil {
			return reconcile.Result{}, err
		}
		*base = *d.DeepCopy()
		on = false
	}

	// 2. Authorize. Only a start asks the directory.
	starting := on && cur == 0
	auth := r.authorize(ctx, d, starting)
	if on && cur > 0 && auth.Status == metav1.ConditionFalse {
		if err := lifecycle.Stop(ctx, r.Client, d, v1alpha1.StopLDAP, r.now()); err != nil {
			return reconcile.Result{}, err
		}
		*base = *d.DeepCopy()
		on = false
	}

	// 5. Replicas.
	var want int32
	if on && (cur > 0 || auth.AllowStart) {
		want = 1
	}

	// 3. PVC, when starting.
	var pvcErr string
	var result reconcile.Result
	if want == 1 && cur == 0 {
		var err error
		if pvcErr, err = r.ensurePVC(ctx, d); err != nil {
			return reconcile.Result{}, err
		}
		if pvcErr != "" {
			want = 0
			result.RequeueAfter = 10 * time.Second // nothing watches PVCs; recheck for a label
		}
	}

	// 4. Service and Deployment.
	if err := r.ensureService(ctx, d); err != nil {
		return reconcile.Result{}, err
	}

	// 6. Stop recording: someone else set Off while the desktop ran.
	if cur > 0 && want == 0 && !lifecycle.StopRecorded(d) {
		if err := lifecycle.Stop(ctx, r.Client, d, v1alpha1.StopAdmin, r.now()); err != nil {
			return reconcile.Result{}, err
		}
		*base = *d.DeepCopy()
	}

	restartPending := d.Spec.RestartRequestedAt != nil &&
		(d.Status.ObservedRestartAt == nil || d.Spec.RestartRequestedAt.After(d.Status.ObservedRestartAt.Time))
	plan := planDeploy(depExists, cur, want, restartPending)
	desired := render.Deployment(cfg, d, want)
	desiredHash := desired.Annotations[names.AnnotationTemplateHash]

	// Record lastStart and the consumed restart before the Deployment changes, so a failed
	// status write cannot lose them after the pod is already starting.
	if plan.SetLastStart {
		now := metav1.NewMicroTime(r.now())
		d.Status.LastStart = &now
	}
	if plan.ConsumeRestart {
		t := *d.Spec.RestartRequestedAt
		d.Status.ObservedRestartAt = &t
	}
	if err := r.patchStatus(ctx, d, base); err != nil {
		return reconcile.Result{}, err
	}

	applied, err := r.writeDeployment(ctx, d, dep, depExists, desired, plan, want)
	if err != nil {
		return reconcile.Result{}, err
	}

	return result, r.finish(ctx, d, base, pods.Items, pod, finishArgs{
		on: on, want: want, auth: auth, errMsg: pvcErr, appliedHash: applied, desiredHash: desiredHash,
	})
}

type finishArgs struct {
	on          bool
	want        int32
	auth        authResult
	errMsg      string
	appliedHash string
	desiredHash string
}

// finish computes and patches the status (step 8).
func (r *Reconciler) finish(ctx context.Context, d, base *v1alpha1.Desktop, pods []corev1.Pod, pod *corev1.Pod, a finishArgs) error {
	st := &d.Status
	prevPhase := st.Phase

	// Pod identity; a new pod starts with nothing reported.
	if pod == nil {
		st.PodName, st.PodUID = "", ""
		st.AgentReady, st.LastReportTime, st.BootProgress = false, nil, 0
	} else if string(pod.UID) != st.PodUID {
		st.PodName, st.PodUID = pod.Name, string(pod.UID)
		st.AgentReady, st.LastReportTime, st.BootProgress = false, nil, 0
	}

	in := statusInput{
		Run:               a.want > 0,
		ErrMsg:            a.errMsg,
		PodExists:         len(pods) > 0,
		Pod:               pod,
		RequireAgentReady: *r.Config().Controller.RequireAgentReady,
		AgentReady:        st.AgentReady,
		AgentReported:     st.LastReportTime != nil,
		AgentStage:        st.Stage,
		AgentProgress:     st.BootProgress,
	}
	if a.on && a.want == 0 && a.errMsg == "" {
		in.Note = a.auth.Message
	}
	res := computeStatus(in)
	st.Phase, st.Stage, st.Message, st.BootProgress = res.Phase, res.Stage, res.Message, res.Progress

	st.URL = render.ServiceURL(r.Config(), d)
	if a.appliedHash != "" {
		st.AppliedTemplateHash = a.appliedHash
		st.UpdatePending = a.desiredHash != a.appliedHash
	}
	st.ObservedGeneration = d.Generation

	meta.SetStatusCondition(&st.Conditions, metav1.Condition{
		Type: v1alpha1.ConditionAuthorized, Status: a.auth.Status, Reason: a.auth.Reason,
		Message: a.auth.Message, ObservedGeneration: d.Generation,
	})
	ready := metav1.ConditionFalse
	if res.Phase == v1alpha1.PhaseRunning {
		ready = metav1.ConditionTrue
	}
	meta.SetStatusCondition(&st.Conditions, metav1.Condition{
		Type: v1alpha1.ConditionReady, Status: ready, Reason: string(res.Phase),
		Message: res.Message, ObservedGeneration: d.Generation,
	})

	if err := r.patchStatus(ctx, d, base); err != nil {
		return err
	}
	if res.Phase == v1alpha1.PhaseRunning && prevPhase != v1alpha1.PhaseRunning && st.LastStart != nil {
		if prev, ok := r.startObserved.Swap(d.Name, st.LastStart.Time); ok && prev.(time.Time).Equal(st.LastStart.Time) {
			return nil
		}
		if secs := r.now().Sub(st.LastStart.Time).Seconds(); secs >= 0 {
			metrics.StartDuration.Observe(secs)
		}
	}
	return nil
}

// ensurePVC creates the PVC or adopts an existing one. It returns a message when the PVC
// belongs to something else. It never deletes a PVC and never sets an ownerReference.
func (r *Reconciler) ensurePVC(ctx context.Context, d *v1alpha1.Desktop) (string, error) {
	desired := render.PVC(r.Config(), d)
	var pvc corev1.PersistentVolumeClaim
	err := r.APIReader.Get(ctx, client.ObjectKeyFromObject(desired), &pvc)
	if apierrors.IsNotFound(err) {
		return "", r.Create(ctx, desired)
	}
	if err != nil {
		return "", err
	}
	if pvc.Labels[names.LabelDesktop] != d.Name {
		return fmt.Sprintf("PVC %s exists but is not labeled %s=%s; label it to adopt it", pvc.Name, names.LabelDesktop, d.Name), nil
	}
	orig := pvc.DeepCopy()
	addMissing(&pvc.Labels, desired.Labels)
	addMissing(&pvc.Annotations, desired.Annotations)
	if equality.Semantic.DeepEqual(orig.ObjectMeta, pvc.ObjectMeta) {
		return "", nil
	}
	return "", r.Patch(ctx, &pvc, client.MergeFrom(orig))
}

func addMissing(dst *map[string]string, src map[string]string) {
	for k, v := range src {
		if _, ok := (*dst)[k]; ok {
			continue
		}
		if *dst == nil {
			*dst = map[string]string{}
		}
		(*dst)[k] = v
	}
}

func (r *Reconciler) ensureService(ctx context.Context, d *v1alpha1.Desktop) error {
	desired := render.Service(r.Config(), d)
	svc := &corev1.Service{}
	err := r.Get(ctx, client.ObjectKeyFromObject(desired), svc)
	if apierrors.IsNotFound(err) {
		if err := controllerutil.SetControllerReference(d, desired, r.Scheme()); err != nil {
			return err
		}
		return r.Create(ctx, desired)
	}
	if err != nil {
		return err
	}
	orig := svc.DeepCopy()
	if err := controllerutil.SetControllerReference(d, svc, r.Scheme()); err != nil {
		return err
	}
	for k, v := range desired.Labels {
		if svc.Labels == nil {
			svc.Labels = map[string]string{}
		}
		svc.Labels[k] = v
	}
	svc.Spec.Selector = desired.Spec.Selector
	if len(svc.Spec.Ports) != 1 || svc.Spec.Ports[0].Port != desired.Spec.Ports[0].Port ||
		svc.Spec.Ports[0].TargetPort != desired.Spec.Ports[0].TargetPort || svc.Spec.Ports[0].Name != desired.Spec.Ports[0].Name {
		svc.Spec.Ports = desired.Spec.Ports
	}
	if equality.Semantic.DeepEqual(orig, svc) {
		return nil
	}
	return r.Update(ctx, svc)
}

// writeDeployment creates or updates the Deployment per plan and returns the applied template hash.
func (r *Reconciler) writeDeployment(ctx context.Context, d *v1alpha1.Desktop, dep *appsv1.Deployment, exists bool,
	desired *appsv1.Deployment, plan deployPlan, want int32) (string, error) {
	desiredHash := desired.Annotations[names.AnnotationTemplateHash]

	if !exists {
		if plan.StampRestart {
			stampRestart(desired, d)
		}
		if err := controllerutil.SetControllerReference(d, desired, r.Scheme()); err != nil {
			return "", err
		}
		return desiredHash, r.Create(ctx, desired)
	}

	applied := dep.Annotations[names.AnnotationTemplateHash]
	cur := replicasOf(dep)

	if plan.WriteTemplate {
		orig := dep.DeepCopy()
		// Keep the existing restarted-at unless a restart is applied now, so the template
		// only changes when something real changed.
		if prev, ok := dep.Spec.Template.Annotations[names.AnnotationRestartedAt]; ok {
			desired.Spec.Template.Annotations[names.AnnotationRestartedAt] = prev
		}
		if plan.StampRestart {
			stampRestart(desired, d)
		}
		if err := controllerutil.SetControllerReference(d, dep, r.Scheme()); err != nil {
			return "", err
		}
		for k, v := range desired.Labels {
			if dep.Labels == nil {
				dep.Labels = map[string]string{}
			}
			dep.Labels[k] = v
		}
		if dep.Annotations == nil {
			dep.Annotations = map[string]string{}
		}
		for k, v := range desired.Annotations {
			dep.Annotations[k] = v
		}
		changed := applied != desiredHash || cur != want || plan.StampRestart ||
			!equality.Semantic.DeepEqual(orig.Labels, dep.Labels) || !equality.Semantic.DeepEqual(orig.OwnerReferences, dep.OwnerReferences) ||
			orig.Annotations[names.AnnotationUser] != dep.Annotations[names.AnnotationUser]
		if !changed {
			return desiredHash, nil
		}
		dep.Spec.Replicas = &want
		dep.Spec.Strategy = desired.Spec.Strategy
		dep.Spec.Template = desired.Spec.Template
		// A merge patch, not Update: no resourceVersion, so status bumps by the Deployment
		// controller do not make the write conflict.
		return desiredHash, r.Patch(ctx, dep, client.MergeFrom(orig))
	}

	// A running desktop: scale only.
	if cur != want {
		patch, _ := json.Marshal(map[string]any{"spec": map[string]any{"replicas": want}})
		if err := r.Patch(ctx, dep, client.RawPatch(types.MergePatchType, patch)); err != nil {
			return "", err
		}
	}
	return applied, nil
}

// replicasOf is the Deployment's replicas; unset means 1.
func replicasOf(dep *appsv1.Deployment) int32 {
	if dep.Spec.Replicas == nil {
		return 1
	}
	return *dep.Spec.Replicas
}

func stampRestart(dep *appsv1.Deployment, d *v1alpha1.Desktop) {
	dep.Spec.Template.Annotations[names.AnnotationRestartedAt] = d.Spec.RestartRequestedAt.UTC().Format(time.RFC3339)
}
