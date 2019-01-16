# Ctraffic - A continous-traffic test program

This program targets tests of disturbances for instance fail-over,
rolling upgrades and network problems. `Ctraffic` generates continuous
traffic over a period of time and monitors problems such as lost
connections, traffic disturbancies and retransmissions.

A `ctraffic` server is started, usually in a cluster behind some load
balancer for instance a [Kubernetes](https://kubernetes.io/)
service. Then `ctraffic` is started in client mode to generate traffic
on a number of connection. This command runs 100 KB/sec over a 1
minute period on 200 connections;

```
> ctraffic -timeout 1m -address [1000::1]:5003 -rate 100 -nconn 200 \
  -monitor_interval 1s | jq .
Conn N/fail/failedconnect: 200/0/0, Packets send/rec/dropped: 99/99/0
Conn N/fail/failedconnect: 200/0/0, Packets send/rec/dropped: 197/197/0
Conn N/fail/failedconnect: 200/0/0, Packets send/rec/dropped: 299/299/0
...
Conn N/fail/failedconnect: 200/0/0, Packets send/rec/dropped: 5917/5917/0
Conn N/fail/failedconnect: 200/0/0, Packets send/rec/dropped: 5997/5997/0
{
  "Started": "2019-01-16T09:40:04.030614577Z",
  "Duration": 59996714439,
  "Rate": 100,
  "Connections": 200,
  "PacketSize": 1024,
  "FailedConn": 0,
  "Sent": 5997,
  "Received": 5997,
  "FailedConnects": 0,
  "Dropped": 0,
  "SendRate": 99.95547349675762,
  "Throughput": 99.95547349675762
}
```

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

## Statistics

At the end of a test run statistics is printed to `stdout` in
(unformatted) `json` format. Pipe to `jq` to get formatted output.
The "monitor" printouts goes to `stderr` so they are visible and will
not interfere with the statistics output;

```
> ctraffic ... -monitor_interval 1s | jq .
...
{
  "Started": "2019-01-16T09:40:04.030614577Z",
  "Duration": 59996714439,
  "Rate": 100,
  "Connections": 200,
  "PacketSize": 1024,
  "FailedConn": 0,
  "Sent": 5997,
  "Received": 5997,
  "FailedConnects": 0,
  "Dropped": 0,
  "SendRate": 99.95547349675762,
  "Throughput": 99.95547349675762
}
```

`SendRate` and `Throughput` are computed values from packets `Sent`
and `Received` respectively.

`Dropped` packets is not the difference sent and received packets but
packets deliberate droppes on the sending side because of
disturbancies such as network delays (caused by
packet-loss/retransmit).


## Build

```
go get github.com/brucespang/go-tcpinfo
go get golang.org/x/time/rate
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
