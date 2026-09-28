TAG?=latest

mod-download:
	go mod download

build-docker:
	docker build -t ghcr.io/joaopaulosr95/k8s-workload-hardening:$(TAG) .
.PHONY: test cover verify-crd

test:
	go test ./pkg/... -race

cover:
	go test ./pkg/... -coverprofile=coverage.out
	go tool cover -func=coverage.out | tail -1

verify-crd:
	./hack/verify-crd.sh
