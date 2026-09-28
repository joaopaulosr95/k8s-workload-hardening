TAG?=latest

mod-download:
	go mod download

build-docker:
	docker build -t ghcr.io/joaopaulosr95/k8s-workload-hardening:$(TAG) .