FROM dva-registry.internal.salesforce.com/sfci/docker-images/golang_build:122 as builder
MAINTAINER suyashtava@gmail.com
LABEL description="This is the Docker image which upgrades statefulsets using OnDelete strategy."

ADD ./  /stshome/
WORKDIR /stshome
RUN env GOOS=linux GOARCH=amd64 go build  -mod vendor

FROM dva-registry.internal.salesforce.com/sfci/docker-images/sfdc_centos7_tini:61

RUN mkdir /stshome
COPY --from=builder  /stshome/stsupgrader   /stshome/
WORKDIR /stshome

# Use tini to handle SIGTERM correctly.
ENTRYPOINT [ "/tini", "-g", "--" ]
