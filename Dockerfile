FROM scratch
COPY --chown=0:0 image/ /
CMD ["/ctraffic", "-server", "-udp", "-address=[::]:5003"]
