
build:
	CGO_ENABLED=0  GOOS=linux  go build  -installsuffix cgo -ldflags '-w' -o syncrets .

clean:
	rm -rf syncrets

image: build
	podman build  -t localhost/syncrets:latest .

TEMP_FILE := $(shell mktemp  --suffix .tar)
push-image: image
	podman save localhost/syncrets:latest > ${TEMP_FILE}
	minikube -p $(CLUSTER) image load ${TEMP_FILE}

deploy: push-image
	kubectl --context $(CLUSTER) apply  -f deployment/syncrets.yaml

redeploy: push-image
	kubectl --context $(CLUSTER) delete pod -lapp=syncrets -n cert-manager
