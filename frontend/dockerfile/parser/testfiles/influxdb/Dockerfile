FROM ubuntu:14.04

RUN apt-get update && apt-get install --yes --force-yes apt-transport-https curl
RUN curl -sL https://repos.influxdata.com/influxdb.key | apt-key add -
RUN /bin/bash -c 'source /etc/lsb-release && echo "deb https://repos.influxdata.com/${DISTRIB_ID,,} ${DISTRIB_CODENAME} stable"' | tee "/etc/apt/sources.list.d/influxdb.list"
RUN apt-get update && sudo apt-get install influxdb

CMD service influxdb start

EXPOSE 8086
EXPOSE 8088
EXPOSE 8089/udp
