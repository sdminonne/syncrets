
build:
	go build -o syncrets .

clean:
	rm -rf syncrets

image:
	podman build  -t localhost/resource-copier:latest .

TEMP_FILE := $(shell mktemp  --suffix .tar)
deploy:
	podman save localhost/resource-copier:latest > ${TEMP_FILE}
	minikube -p mgmt image load ${TEMP_FILE}