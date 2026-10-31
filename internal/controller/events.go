// SPDX-License-Identifier: Apache-2.0

package controller

import (
	"context"
	"regexp"
	"strconv"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/lhns/kube-webtop-workspaces/api/v1alpha1"
	"github.com/lhns/kube-webtop-workspaces/internal/names"
)

// eventPodUIDIndex is the field index of Events by the UID of the Pod they are about.
const eventPodUIDIndex = "involvedObject.uid"

// Kubelet and attach/detach controller event reasons that refine the CreatingContainer stage.
const (
	reasonPulling             = "Pulling"
	reasonPulled              = "Pulled"
	reasonFailedAttachVolume  = "FailedAttachVolume"
	reasonSuccessfulAttachVol = "SuccessfulAttachVolume"
)

// podEvents is what the events of one pod say about its start.
type podEvents struct {
	// AttachFailing is true when a FailedAttachVolume has no later SuccessfulAttachVolume.
	AttachFailing bool
	// Pulling is true when a Pulling event has no later Pulled; PullImage and PullStarted describe it.
	Pulling     bool
	PullImage   string
	PullStarted time.Time
	// Pulled is the last completed pull that reported a size and duration, or nil.
	Pulled *pulled
}

// pulled is a completed image pull.
type pulled struct {
	Image    string
	Size     int64
	Duration time.Duration
}

var (
	quoted     = regexp.MustCompile(`"([^"]+)"`)
	pulledRe   = regexp.MustCompile(`Successfully pulled image "([^"]+)" in (\S+)`)
	imageSizeR = regexp.MustCompile(`Image size: (\d+) bytes`)
)

// eventTime is the latest time an event reports; kubelet events are deduplicated, so
// LastTimestamp moves while FirstTimestamp stays.
func eventTime(e *corev1.Event) time.Time {
	switch {
	case !e.LastTimestamp.IsZero():
		return e.LastTimestamp.Time
	case !e.EventTime.IsZero():
		return e.EventTime.Time
	case !e.FirstTimestamp.IsZero():
		return e.FirstTimestamp.Time
	}
	return e.CreationTimestamp.Time
}

// parsePulled reads a kubelet "Pulled" message:
//
//	Successfully pulled image "ref" in 9m13.252s (9m13.252s including waiting). Image size: 3745009478 bytes.
//
// It returns nil for "already present on machine" (no pull happened) and for messages without a
// parsable duration; older kubelets leave the size out, which gives Size 0.
func parsePulled(msg string) *pulled {
	m := pulledRe.FindStringSubmatch(msg)
	if m == nil {
		return nil
	}
	d, err := time.ParseDuration(m[2])
	if err != nil {
		return nil
	}
	p := &pulled{Image: m[1], Duration: d}
	if s := imageSizeR.FindStringSubmatch(msg); s != nil {
		p.Size, _ = strconv.ParseInt(s[1], 10, 64)
	}
	return p
}

// summarizeEvents reduces a pod's events to podEvents. Order of the slice does not matter.
func summarizeEvents(events []corev1.Event) podEvents {
	var out podEvents
	var attachFailed, attachOK, pullStart, pullEnd, pulledAt time.Time
	for i := range events {
		e := &events[i]
		t := eventTime(e)
		switch e.Reason {
		case reasonFailedAttachVolume:
			attachFailed = laterOf(attachFailed, t)
		case reasonSuccessfulAttachVol:
			attachOK = laterOf(attachOK, t)
		case reasonPulling:
			if t.After(pullStart) {
				pullStart = t
				out.PullImage = firstQuoted(e.Message)
			}
		case reasonPulled:
			pullEnd = laterOf(pullEnd, t)
			if p := parsePulled(e.Message); p != nil && !t.Before(pulledAt) {
				pulledAt = t
				out.Pulled = p
			}
		}
	}
	// On a tie the later-phase event wins: a success in the same second as a failure ends it.
	out.AttachFailing = !attachFailed.IsZero() && attachFailed.After(attachOK)
	out.Pulling = !pullStart.IsZero() && pullStart.After(pullEnd)
	if out.Pulling {
		out.PullStarted = pullStart
	} else {
		out.PullImage = ""
	}
	return out
}

func laterOf(a, b time.Time) time.Time {
	if b.After(a) {
		return b
	}
	return a
}

func firstQuoted(s string) string {
	if m := quoted.FindStringSubmatch(s); m != nil {
		return m[1]
	}
	return ""
}

func relevantReason(r string) bool {
	switch r {
	case reasonPulling, reasonPulled, reasonFailedAttachVolume, reasonSuccessfulAttachVol:
		return true
	}
	return false
}

// desktopImage is the image of the desktop container of pod, as the kubelet names it in events.
func desktopImage(pod *corev1.Pod) string {
	for _, c := range pod.Spec.Containers {
		if c.Name == names.ContainerName {
			return c.Image
		}
	}
	return ""
}

// updateImagePull returns the image pull record after seeing ev for a pod running image. A record
// of another image ref is dropped, so a size and duration never describe the wrong image.
func updateImagePull(prev *v1alpha1.ImagePull, image string, ev podEvents) *v1alpha1.ImagePull {
	if image == "" {
		return prev
	}
	ip := &v1alpha1.ImagePull{Image: image}
	if prev != nil && prev.Image == image {
		ip = prev.DeepCopy()
	}
	if p := ev.Pulled; p != nil && p.Image == image {
		ip.LastDurationSeconds = max(1, int64(p.Duration.Round(time.Second).Seconds()))
		if p.Size > 0 {
			ip.LastSizeBytes = p.Size
		}
	}
	ip.StartedAt = nil
	if ev.Pulling && ev.PullImage == image {
		t := metav1.NewTime(ev.PullStarted)
		ip.StartedAt = &t
	}
	return ip
}

// nextImagePull is updateImagePull plus, while pulling an image this desktop never pulled, the
// size and duration another Desktop recorded for the same image ref.
func (r *Reconciler) nextImagePull(ctx context.Context, ns string, prev *v1alpha1.ImagePull, image string, ev podEvents, pulling bool) *v1alpha1.ImagePull {
	ip := updateImagePull(prev, image, ev)
	if ip == nil || !pulling || ip.LastDurationSeconds > 0 {
		return ip
	}
	var all v1alpha1.DesktopList
	if err := r.List(ctx, &all, client.InNamespace(ns)); err != nil {
		return ip // an estimate is optional
	}
	for i := range all.Items {
		if o := all.Items[i].Status.ImagePull; o != nil && o.Image == image && o.LastDurationSeconds > 0 {
			ip.LastDurationSeconds, ip.LastSizeBytes = o.LastDurationSeconds, o.LastSizeBytes
			break
		}
	}
	return ip
}
