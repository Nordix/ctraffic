# Ctraffic - A continous-traffic test program

This program targets tests of disturbances for instance fail-over,
rolling upgrades and network problems. `Ctraffic` generates continuous
traffic over a period of time and monitors problems such as lost
connections, traffic disturbancies and retransmissions.

Even though `ctraffic` is pretty fast it is not a performance
measurement tool since some bottlenecks are likely in `ctraffic`
itself.

A ctraffic *server* is started, usually in a cluster behind some load
balancer for instance a [Kubernetes](https://kubernetes.io/)
service. Then ctraffic is started in *client* mode to generate traffic
on a number of connection. This command runs 100 KB/sec over a 1
minute period on 200 connections;

```
> ctraffic -timeout 1m -address [1000::1]:5003 -rate 100 -nconn 200 -monitor | jq .
Conn act/fail/connecting: 200/0/0, Packets send/rec/dropped: 86/86/0
Conn act/fail/connecting: 200/0/0, Packets send/rec/dropped: 194/194/0
Conn act/fail/connecting: 200/0/0, Packets send/rec/dropped: 286/286/0
...
Conn act/fail/connecting: 200/0/0, Packets send/rec/dropped: 5816/5816/0
Conn act/fail/connecting: 200/0/0, Packets send/rec/dropped: 5908/5908/0
{
  "Started": "2019-01-21T08:40:08.293474852Z",
  "Duration": 59993832043,
  "Rate": 100,
  "Connections": 200,
  "PacketSize": 1024,
  "FailedConnections": 0,
  "Sent": 5994,
  "Received": 5994,
  "Dropped": 0,
  "Retransmits": 0,
  "FailedConnects": 0
}
```

See also the `xcluster` [ovl/ctraffic](https://github.com/Nordix/xcluster/tree/master/ovl/ctraffic).

## Usage

Basic usage;
```
# Start the server;
ctraffic -server
# In another shell;
ctraffic -nconn 100 -monitor
```

In Kubernetes;
```
kubectl apply -f https://github.com/Nordix/ctraffic/raw/master/ctraffic.yaml
# Check the external ip;
kubectl get svc ctraffic
# On some external machine;
externalip=....      # from the printout above
ctraffic -timeout 1m -address $externalip:5003 -rate 100 -nconn 200 -monitor
```

## Source addresses

To test may connections from a single source (the default) is many
times a bad test case. Many source addresses can be specified with the
`-srccidr` option in the same way as for
[mconnect](https://github.com/Nordix/mconnect#many-source-addresses).

Another option is to specify the source addresses (optionally
including ports) in a file and use the `-srcfile` option. An address
file may look like;

```
[1000::1:10.200.200.0]:7000
[1000::1:10.200.200.1]:7000
[1000::1:10.200.200.2]:7000
[1000::1:10.200.200.3]:7000
...
```

If there is not enough addresses in the file `ctraffic` will terminate
with "Ran out of source addresses".


## Analyze saved data

In automatic testing the statistics is saved for later analysis. The
-analyze option has 3 options `throughput|connections|hosts`.

In the example below a local server is used and is killed around 5
seconds after the test is started;

```
# Start the server in a shell;
ctraffic -server
# In another shell start the client;
ctraffic -nconn 40 -rate 10 -stats all -monitor -timeout 12s > /tmp/data.json
# In the first shell, kill the server after ~5s;
^C
```

Analyze the saved data;

```
ctraffic -stat_file /tmp/data.json -analyze connections
Time Active New Failed Connecting
0.5 40 40 0 0
1.5 40 0 0 0
2.5 40 0 0 0
3.5 40 0 0 0
4.5 37 3 3 3
5.5 28 9 9 12
6.5 16 12 12 24
7.5 8 8 8 32
8.5 0 8 8 40
9.5 0 0 0 40
```

Normally `new` and `failed` are the same because failed connections
are immediately renewed. Since the server is stopped in this example
the new connections will go to `connecting`. The packets/second on
each connection is 10/40 which means one packet every 4'th second so
it takes ~4 sec for all connections to fail.

The data can be used to create a plot with `gnuplot`. Below is an
example how to do this maually;

```
ctraffic -stat_file /tmp/data.json -analyze connections > /tmp/data.data
gnuplot
# In gnuplot;
set yrange [0:48]
set xrange [0:10]
set key autotitle columnhead
set boxwidth 0.5 relative
set border 0
set style fill solid 0.5 border -1
plot '/tmp/data.data' using ($1-0.25):2 with boxes, '' using 1:5 with boxes, '' using ($1+0.25):4 with boxes
```

The `scripts/plot.sh` script described later can be used for greating graphs
automatically.

Server hostname data can be analyzed;

```
$ ctraffic -analyze hosts -stat_file /tmp/scale.out 
Lost connections: 26
  vm-001 6
  vm-002 12
  vm-004 2
  vm-005 4
  vm-010 2
Lasting connections: 100
  vm-003 10
  vm-004 11
  vm-005 11
  vm-006 13
  vm-007 16
  vm-008 13
  vm-009 16
  vm-010 10
```

## Statistics

At the end of a test run statistics is printed to `stdout` in
(unformatted) `json` format. Pipe to `jq` to get formatted output.
The "monitor" printouts goes to `stderr` so they are visible and will
not interfere with the statistics output;

```
> ctraffic ... -monitor | jq .
...
{
  "Started": "2019-01-21T08:40:08.293474852Z",
  "Duration": 59993832043,
  "Rate": 100,
  "Connections": 200,
  "PacketSize": 1024,
  "FailedConnections": 0,
  "Sent": 5994,
  "Received": 5994,
  "Dropped": 0,
  "Retransmits": 0,
  "FailedConnects": 0
}
```

If sent and received packets packet counters differs packets have been
lost "in flight" when connections fails.

`Dropped` are packets deliberate dropped on the sending side because
of disturbancies such as network delays (caused by
packet-loss/retransmit).

<img src="docs/ctraffic-drop.svg" alt="Figure drop-packes" width="80%" />

If the interval between packets is larger than the re-transmit
interval, usually 200mS on Linux, no packet will be dropped on a
single packet-loss. If you want to see packet loss as dropped
packets (and reduced throughput) make sure the packet rate per
connection is higher than 5 packets/S.

If `--stats=all` is specified additional statistics for connections
and samples are included. This is necessary for post-test analysis.


## Graphs

The `scripts/plot.sh` script is a utility for creating graphs. Example;

```
scripts/plot.sh connections Active < /tmp/data.json > /tmp/conn.svg
```

with the data from the example above produces this graph;

<img src="docs/example-connection.svg" alt="example graph" width="65%" />

Here is another example where packet-loss is introduced for some time
with;

```
iptables -A INPUT -i eth1 -m statistic --mode random --probability 0.05 -j DROP
```

Traffic at 500 KB/S is started towards a k8s cluster and the
statistics is saved;

```
ctraffic -timeout 30s -address 10.0.0.2:5003 -nconn 40 -rate 500 -stats=all > /tmp/packet-loss.json
```

The graph is produced with;

```
./scripts/plot.sh throughput < /tmp/packet-loss.json > /tmp/packet-loss.svg
```

<img src="docs/packet-loss.svg" alt="example graph" width="65%" />


## Build

```
go get -u github.com/Nordix/ctraffic
ver=$(date +%F:%T)
CGO_ENABLED=0 GOOS=linux go install -a \
  -ldflags "-extldflags '-static' -X main.version=$ver" \
  github.com/Nordix/ctraffic/cmd/ctraffic
strip $GOPATH/bin/ctraffic

# Build a docker image;
./build.sh image --image=registry.nordix.org/cloud-native/ctraffic --version=latest
```


## Problems

The `net.Conn` on the server side opens 3 file descriptors (1 socket +
2 pipes) so even though the "ulimit" is 1024 fd's only ~330 simultaneous
connections cen be served.
