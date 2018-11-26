manifests:
	go run vendor/sigs.k8s.io/controller-tools/cmd/controller-gen/main.go all

generate:
	go generate ./pkg/... ./cmd/...
