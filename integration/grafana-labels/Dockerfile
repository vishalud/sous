FROM ubuntu:latest

ENV package grafana-1.9.1
RUN apt-get update
RUN DEBIAN_FRONTEND=noninteractive apt-get install -y curl mini-httpd uuid-runtime

RUN curl -s http://grafanarel.s3.amazonaws.com/$package.tar.gz | tar -xz --strip-components=1 -C /srv
COPY config.js /srv/
RUN rm /srv/config.sample.js

WORKDIR /

LABEL \
  com.opentable.sous.repo_url=github.com/opentable/docker-grafana \
  com.opentable.sous.repo_offset= \
  com.opentable.sous.version=0.0.17-maybeuseful \
  com.opentable.sous.revision=91495f1b1630084e301241100ecf2e775f6b672c

CMD mini-httpd -d /srv -p $PORT0 -D
