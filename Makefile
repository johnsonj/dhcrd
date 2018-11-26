all: manifests generate

manifests:
	go run vendor/sigs.k8s.io/controller-tools/cmd/controller-gen/main.go all

generate:
	go generate ./pkg/... ./cmd/...

win:
	GOOS=windows go build cmd/manager/main.go