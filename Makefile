.PHONY: build
build:
	-docker buildx create --name coffee-ctl-builder --driver docker-container --bootstrap --platform linux/amd64,linux/arm64
	docker buildx use coffee-ctl-builder
	docker buildx build --platform linux/amd64,linux/arm64 -t mfreudenberg/coffee-ctl:latest --push .
	docker buildx build -t coffee-ctl --load .

.PHONY: build-local
build-local:
	CGO_ENABLED=0 go build -o build/coffee-ctl ./cmd/coffee-ctl

.PHONY: modules
modules:
	go mod tidy

.PHONY: run
run:
	go run cmd/coffee-ctl/*.go

.PHONY: up
up:
	docker-compose up --build
