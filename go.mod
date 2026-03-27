module github.com/example/vault-etcd-sync-operator

go 1.22

require (
	github.com/go-logr/logr v1.4.2
	github.com/hashicorp/vault/api v1.14.0
	github.com/prometheus/client_golang v1.19.1
	go.etcd.io/etcd/client/v3 v3.5.17
	k8s.io/api v0.31.3
	k8s.io/apimachinery v0.31.3
	k8s.io/client-go v0.31.3
	sigs.k8s.io/controller-runtime v0.19.3
)

// Run `go mod tidy` after checkout to populate indirect dependencies.
// Key indirect deps pulled in by controller-runtime / etcd / vault:
//   google.golang.org/grpc, github.com/coreos/go-semver, go.uber.org/zap, etc.
