// SPDX-License-Identifier: Apache-2.0

// Package render builds the desired PVC, Service and Deployment of a desktop from the config
// (docs/contract.md, sections 3.1 and 5). Pure functions: no client access.
package render

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"maps"
	"strconv"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/intstr"

	"github.com/lhns/kube-webtop-workspaces/api/v1alpha1"
	"github.com/lhns/kube-webtop-workspaces/internal/config"
	"github.com/lhns/kube-webtop-workspaces/internal/names"
)

const (
	volData  = "data"
	volRun   = "run"
	volShm   = "shm"
	volSA    = "webtop-sa"
	portName = "http"
)

func ptr[T any](v T) *T { return &v }

// ObjectName is the name of the Deployment, Service and PVC of d.
func ObjectName(cfg *config.Config, d *v1alpha1.Desktop) string {
	return names.Object(cfg.Names.Prefix, d.Name)
}

// ServiceURL is the proxy target of d.
func ServiceURL(cfg *config.Config, d *v1alpha1.Desktop) string {
	return fmt.Sprintf("http://%s.%s.svc:%d", ObjectName(cfg, d), cfg.Namespace, cfg.Desktop.Port)
}

// merge copies the maps in order; later ones win.
func merge(ms ...map[string]string) map[string]string {
	out := map[string]string{}
	for _, m := range ms {
		maps.Copy(out, m)
	}
	return out
}

// PVC is the desired claim. It never carries an ownerReference.
func PVC(cfg *config.Config, d *v1alpha1.Desktop) *corev1.PersistentVolumeClaim {
	pvc := &corev1.PersistentVolumeClaim{
		ObjectMeta: metav1.ObjectMeta{
			Name:      ObjectName(cfg, d),
			Namespace: cfg.Namespace,
			Labels:    merge(cfg.Storage.Labels, names.Labels(d.Name)),
			Annotations: merge(map[string]string{names.AnnotationUser: d.Spec.User},
				cfg.Storage.Annotations),
		},
		Spec: corev1.PersistentVolumeClaimSpec{
			AccessModes: append([]corev1.PersistentVolumeAccessMode(nil), cfg.Storage.AccessModes...),
			Resources: corev1.VolumeResourceRequirements{
				Requests: corev1.ResourceList{corev1.ResourceStorage: cfg.Storage.Size},
			},
		},
	}
	if cfg.Storage.StorageClassName != "" {
		pvc.Spec.StorageClassName = ptr(cfg.Storage.StorageClassName)
	}
	return pvc
}

// Service is the desired ClusterIP service.
func Service(cfg *config.Config, d *v1alpha1.Desktop) *corev1.Service {
	return &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name:      ObjectName(cfg, d),
			Namespace: cfg.Namespace,
			Labels:    names.Labels(d.Name),
		},
		Spec: corev1.ServiceSpec{
			Type:     corev1.ServiceTypeClusterIP,
			Selector: names.Selector(d.Name),
			Ports: []corev1.ServicePort{{
				Name:       portName,
				Port:       cfg.Desktop.Port,
				TargetPort: intstr.FromInt32(cfg.Desktop.Port),
				Protocol:   corev1.ProtocolTCP,
			}},
		},
	}
}

func defaultSecurityContext() *corev1.SecurityContext {
	return &corev1.SecurityContext{
		RunAsUser:       ptr(int64(0)),
		RunAsNonRoot:    ptr(false),
		Capabilities:    &corev1.Capabilities{Add: []corev1.Capability{"SYS_ADMIN"}},
		ProcMount:       ptr(corev1.UnmaskedProcMount),
		SeccompProfile:  &corev1.SeccompProfile{Type: corev1.SeccompProfileTypeRuntimeDefault},
		AppArmorProfile: &corev1.AppArmorProfile{Type: corev1.AppArmorProfileTypeUnconfined},
	}
}

// PodTemplate is the desired pod template, without the restarted-at annotation.
func PodTemplate(cfg *config.Config, d *v1alpha1.Desktop) corev1.PodTemplateSpec {
	dc := cfg.Desktop
	sc := dc.SecurityContext
	if sc == nil {
		sc = defaultSecurityContext()
	} else {
		sc = sc.DeepCopy()
	}

	env := []corev1.EnvVar{
		{Name: "WEBTOP_CONTROLLER_URL", Value: cfg.PowerAPI.URL},
		{Name: "WEBTOP_TOKEN_PATH", Value: names.TokenPath},
		{Name: "WEBTOP_DESKTOP", Value: d.Name},
		{Name: "WEBTOP_USER", Value: d.Spec.User},
		{Name: "WEBTOP_NAMESPACE", Value: cfg.Namespace},
		{Name: "WEBTOP_HTTP_PORT", Value: strconv.Itoa(int(dc.Port))},
		{Name: "WEBTOP_PERSIST_PATH", Value: cfg.Storage.MountPath},
	}
	env = append(env, dc.ExtraEnv...)

	mounts := []corev1.VolumeMount{
		{Name: volData, MountPath: cfg.Storage.MountPath},
		{Name: volRun, MountPath: "/run"},
		{Name: volShm, MountPath: "/dev/shm"},
		{Name: volSA, MountPath: names.TokenDir, ReadOnly: true},
	}
	mounts = append(mounts, dc.ExtraVolumeMounts...)

	volumes := []corev1.Volume{
		{Name: volData, VolumeSource: corev1.VolumeSource{PersistentVolumeClaim: &corev1.PersistentVolumeClaimVolumeSource{
			ClaimName: ObjectName(cfg, d),
		}}},
		{Name: volRun, VolumeSource: corev1.VolumeSource{EmptyDir: &corev1.EmptyDirVolumeSource{
			Medium: corev1.StorageMediumMemory, SizeLimit: ptr(dc.RunSizeLimit.DeepCopy()),
		}}},
		{Name: volShm, VolumeSource: corev1.VolumeSource{EmptyDir: &corev1.EmptyDirVolumeSource{
			Medium: corev1.StorageMediumMemory, SizeLimit: ptr(dc.ShmSize.DeepCopy()),
		}}},
		{Name: volSA, VolumeSource: corev1.VolumeSource{Projected: &corev1.ProjectedVolumeSource{
			Sources: []corev1.VolumeProjection{{ServiceAccountToken: &corev1.ServiceAccountTokenProjection{
				Audience:          cfg.PowerAPI.Audience,
				ExpirationSeconds: ptr(int64(3600)),
				Path:              names.TokenFile,
			}}},
		}}},
	}
	volumes = append(volumes, dc.ExtraVolumes...)

	container := corev1.Container{
		Name:            names.ContainerName,
		Image:           dc.Image,
		ImagePullPolicy: dc.ImagePullPolicy,
		Ports:           []corev1.ContainerPort{{Name: portName, ContainerPort: dc.Port, Protocol: corev1.ProtocolTCP}},
		Env:             env,
		VolumeMounts:    mounts,
		Resources:       *dc.Resources.DeepCopy(),
		SecurityContext: sc,
		ReadinessProbe: &corev1.Probe{
			ProbeHandler:  corev1.ProbeHandler{TCPSocket: &corev1.TCPSocketAction{Port: intstr.FromInt32(dc.Port)}},
			PeriodSeconds: 5,
		},
	}

	spec := corev1.PodSpec{
		HostUsers:                     dc.HostUsers,
		AutomountServiceAccountToken:  ptr(false),
		EnableServiceLinks:            ptr(false),
		ServiceAccountName:            dc.ServiceAccountName,
		PriorityClassName:             dc.PriorityClassName,
		TerminationGracePeriodSeconds: dc.TerminationGracePeriodSeconds,
		Containers:                    []corev1.Container{container},
		Volumes:                       volumes,
		NodeSelector:                  maps.Clone(dc.NodeSelector),
		Tolerations:                   append([]corev1.Toleration(nil), dc.Tolerations...),
		Affinity:                      dc.Affinity.DeepCopy(),
		ImagePullSecrets:              append([]corev1.LocalObjectReference(nil), dc.ImagePullSecrets...),
	}
	if dc.RuntimeClassName != "" {
		spec.RuntimeClassName = ptr(dc.RuntimeClassName)
	}
	return corev1.PodTemplateSpec{
		ObjectMeta: metav1.ObjectMeta{
			Labels:      merge(dc.PodLabels, names.Labels(d.Name)),
			Annotations: merge(dc.PodAnnotations, map[string]string{names.AnnotationUser: d.Spec.User}),
		},
		Spec: spec,
	}
}

// TemplateHash is the SHA-256 (hex) of the template JSON without the restarted-at annotation.
func TemplateHash(t corev1.PodTemplateSpec) string {
	t = *t.DeepCopy()
	delete(t.Annotations, names.AnnotationRestartedAt)
	b, err := json.Marshal(t)
	if err != nil { // a PodTemplateSpec always marshals
		panic(err)
	}
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

// Deployment is the desired Deployment at the given replicas, carrying the template hash.
// The caller stamps restarted-at on the template when applying a restart.
func Deployment(cfg *config.Config, d *v1alpha1.Desktop, replicas int32) *appsv1.Deployment {
	tpl := PodTemplate(cfg, d)
	return &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{
			Name:      ObjectName(cfg, d),
			Namespace: cfg.Namespace,
			Labels:    names.Labels(d.Name),
			Annotations: map[string]string{
				names.AnnotationUser:         d.Spec.User,
				names.AnnotationTemplateHash: TemplateHash(tpl),
			},
		},
		Spec: appsv1.DeploymentSpec{
			Replicas: ptr(replicas),
			Strategy: appsv1.DeploymentStrategy{Type: appsv1.RecreateDeploymentStrategyType},
			Selector: &metav1.LabelSelector{MatchLabels: names.Selector(d.Name)},
			Template: tpl,
		},
	}
}
