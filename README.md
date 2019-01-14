# Ctraffic - A continous-traffic test program

This program targets tests of disturbances for instance fail-over,
rolling upgrades and network problems. `Ctraffic` generates continuous
traffic over a period of time and monitors problems such as lost
connections, traffic disturbancies and packet loss.

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
```

