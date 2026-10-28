ENVTEST_K8S_VERSION ?= 1.37.x

.PHONY: generate build vet test

# DeepCopy, CRD and the two RBAC roles. Rerun after changing api/ or a +kubebuilder:rbac marker.
generate:
	go tool controller-gen object:headerFile=hack/boilerplate.go.txt paths=./api/...
	go tool controller-gen crd paths=./api/... output:crd:artifacts:config=config/crd
	go tool controller-gen rbac:roleName=kube-webtop-workspaces-controller paths=./internal/controller/... paths=./internal/powerapi/... paths=./internal/ldap/... output:rbac:artifacts:config=config/rbac/controller
	go tool controller-gen rbac:roleName=kube-webtop-workspaces-gateway paths=./internal/gateway/... output:rbac:artifacts:config=config/rbac/gateway

build:
	go build ./...

vet:
	go vet ./...

# envtest needs kube-apiserver and etcd binaries; setup-envtest fetches them.
test:
	KUBEBUILDER_ASSETS="$$(go tool setup-envtest use $(ENVTEST_K8S_VERSION) -p path)" go test ./...
