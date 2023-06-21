
build:
	CGO_ENABLED=0  GOOS=linux  go build  -installsuffix cgo -ldflags '-w' -o syncrets .

clean:
	rm -rf syncrets

image:
	podman build  -t localhost/syncrets:4.0 .

TEMP_FILE := $(shell mktemp  --suffix .tar)
deploy:
	podman save localhost/resource-copier:latest > ${TEMP_FILE}
	minikube -p mgmt image load ${TEMP_FILE}
