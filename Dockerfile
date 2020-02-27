FROM debian:stretch-slim

WORKDIR /

COPY extend-sample-scheduler /usr/local/bin

CMD ["extend-sample-scheduler"]