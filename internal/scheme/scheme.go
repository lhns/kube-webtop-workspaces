// Package scheme builds the runtime.Scheme shared by the controller and the gateway.
package scheme

import (
	"k8s.io/apimachinery/pkg/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"

	"github.com/lhns/kube-webtop-workspaces/api/v1alpha1"
)

// New has the core types and the Desktop API.
func New() (*runtime.Scheme, error) {
	s := runtime.NewScheme()
	if err := clientgoscheme.AddToScheme(s); err != nil {
		return nil, err
	}
	if err := v1alpha1.AddToScheme(s); err != nil {
		return nil, err
	}
	return s, nil
}
