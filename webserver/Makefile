
DOCKERREPO       := registry.cn-beijing.aliyuncs.com/log-service/logtail:ebpf-test-web-server

all: simple-webserver container push

simple-webserver:
	docker run --rm -v $(shell pwd):/go/src/github.com/mrahbar/simple-webserver -w /go/src/github.com/mrahbar/simple-webserver -e CGO_ENABLED=0 golang:1.16 go build -a -installsuffix cgo -o simple-webserver simple-webserver.go

container: simple-webserver
	mkdir -p Dockerbuild && \
	cp -f Dockerfile Dockerbuild/ &&\
	cp -f simple-webserver Dockerbuild/ &&\
	docker build -t $(DOCKERREPO) Dockerbuild/

push: container
	docker push $(DOCKERREPO)