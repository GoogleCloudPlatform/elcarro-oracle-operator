module github.com/GoogleCloudPlatform/elcarro-oracle-operator

go 1.16

require (
	bitbucket.org/creachadair/stringset v0.0.9
	cloud.google.com/go v0.81.0
	cloud.google.com/go/storage v1.10.0
	github.com/bazelbuild/rules_go v0.27.0
	github.com/ghodss/yaml v1.0.0
	github.com/go-logr/logr v0.4.0
	github.com/godror/godror v0.25.3
	github.com/golang/mock v1.5.0
	github.com/golang/protobuf v1.5.2
	github.com/google/go-cmp v0.5.5
	github.com/google/gofuzz v1.2.0 // indirect
	github.com/google/uuid v1.1.2
	github.com/grpc-ecosystem/grpc-health-probe v0.4.2
	github.com/hpcloud/tail v1.0.0
	github.com/imdario/mergo v0.3.11 // indirect
	github.com/kubernetes-csi/external-snapshotter/v2 v2.1.1
	github.com/onsi/ginkgo v1.15.2
	github.com/onsi/gomega v1.11.0
	github.com/pkg/errors v0.9.1
	github.com/prometheus/client_golang v1.7.1
	github.com/prometheus/common v0.13.0 // indirect
	github.com/robfig/cron v1.2.0
	github.com/wadey/gocovmerge v0.0.0-20160331181800-b5bfa59ec0ad
	google.golang.org/api v0.44.0
	google.golang.org/genproto v0.0.0-20210518161634-ec7691c0a37d
	google.golang.org/grpc v1.37.1
	google.golang.org/grpc/cmd/protoc-gen-go-grpc v1.1.0
	google.golang.org/protobuf v1.26.0
	gopkg.in/yaml.v2 v2.4.0
	k8s.io/api v0.20.5
	k8s.io/apiextensions-apiserver v0.20.5 // indirect
	k8s.io/apimachinery v0.20.5
	k8s.io/client-go v0.20.5
	k8s.io/klog/v2 v2.8.0
	k8s.io/repo-infra v0.2.2
	k8s.io/utils v0.0.0-20210305010621-2afb4311ab10
	sigs.k8s.io/controller-runtime v0.8.3
	sigs.k8s.io/controller-tools v0.5.0
	sigs.k8s.io/kustomize/kustomize/v4 v4.1.3
)
