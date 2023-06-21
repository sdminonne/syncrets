FROM busybox
LABEL maintainer "sminonne@redhat.com"
ADD syncrets /syncrets
ENTRYPOINT ["/syncrets"]
