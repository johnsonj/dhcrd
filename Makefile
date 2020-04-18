all: manifests generate

manifests:
	# TODO: generated rbac isn't correct
	go run sigs.k8s.io/controller-tools/cmd/controller-gen crd

generate:
	go generate ./pkg/... ./cmd/...

win:
	GOOS=windows go build cmd/manager/main.go

gazelle-gomod:
	bazel run //:gazelle -- update-repos -from_file=go.mod

gazelle:
	bazel run //:gazelle

push:
	bazel run //cmd/manager:publish
