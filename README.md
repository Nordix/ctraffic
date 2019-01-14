# Ctraffic - A continous-traffic test program

This program targets tests of disturbances for instance fail-over,
rolling upgrades and network problems. `Ctraffic` generates continuous
traffic over a period of time and monitors problems such as lost
connections, traffic disturbancies and packet loss.

A `ctraffic` server is started, usually in a cluster behind some load
balancer for instance a [Kubernetes](https://kubernetes.io/)
service. Then `ctraffic` is started in client mode to generate traffic
on a number of connection;

```
ctraffic -timeout 1m -address [1000::1]:5003 -rate 100 -nconn 200 \
  -monitor_interval 1s
```

runs 1000 KB/sec over a 1 minute period on 200 connections.


**NOTE**: Statistics reporting is not yet implemented-


## Usage

Basic usage;
```
# Start the server;
ctraffic -server
# In another shell;
ctraffic -nconn 100 -monitor_interval 1s
```

In Kubernetes;
```
kubectl apply -f https://github.com/Nordix/ctraffic/raw/master/ctraffic.yaml
# Check the external ip;
kubectl get svc ctraffic
# On some external machine;
externalip=....      # from the printout above
ctraffic -timeout 1m -address $externalip:5003 -rate 100 -nconn 200 \
  -monitor_interval 1s
```

## Build

```
go get -u github.com/Nordix/ctraffic
cd $GOPATH/src/github.com/Nordix/ctraffic
ver=$(git rev-parse --short HEAD)
#ver=$(date +%F:%T)
CGO_ENABLED=0 GOOS=linux go install -a \
  -ldflags "-extldflags '-static' -X main.version=$ver" \
  github.com/Nordix/ctraffic/cmd/ctraffic
strip $GOPATH/bin/ctraffic

# Build a docker image;
docker rmi docker.io/nordixorg/ctraffic:$ver
cd $GOPATH/bin
tar -cf - ctraffic | docker import \
  -c 'CMD ["/ctraffic", "-server", "-address", "[::]:5003"]' \
  - docker.io/nordixorg/ctraffic:$ver
```


## Problems

The `net.Conn` on the server side opens 3 file descriptors (1 socket +
2 pipes) so even though the "ulimit" is 1024 fd's only ~330 simultaneous
connections cen be served.
