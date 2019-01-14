// Project page; https://github.com/Nordix/ctraffic/
// LICENSE; MIT. See the "LICENSE" file in the Project page.
// Copyright (c) 2019, Nordix Foundation

package main

import (
	"context"
	"flag"
	"fmt"
	tcpinfo "github.com/brucespang/go-tcpinfo"
	"golang.org/x/time/rate"
	"io"
	"log"
	"math/rand"
	"net"
	"os"
	"sync"
	"sync/atomic"
	"time"
)

var version string = "unknown"

const helptext = `
Ctraffic setup and maintain and monitors many continuous connections.

Options;
`

type config struct {
	isServer  *bool
	addr      *string
	src       *string
	nconn     *int
	version   *bool
	srcmax    *int
	output    *string
	timeout   *time.Duration
	psize     *int
	rate      *float64
	reconnect *bool
	ctype     *string
}

func main() {
	flag.Usage = func() {
		fmt.Fprintln(flag.CommandLine.Output(), helptext)
		flag.PrintDefaults()
	}

	var cmd config
	cmd.isServer = flag.Bool("server", false, "Act as server")
	cmd.ctype = flag.String("client", "echo", "echo|fake")
	cmd.addr = flag.String("address", "[::1]:5003", "Server address")
	cmd.src = flag.String("src", "", "Base source address use")
	cmd.srcmax = flag.Int("srcmax", 100, "Number of connect sources")
	cmd.nconn = flag.Int("nconn", 1, "Number of connections")
	cmd.version = flag.Bool("version", false, "Print version and quit")
	cmd.output = flag.String("output", "txt", "Output format; json|txt")
	cmd.timeout = flag.Duration("timeout", 10*time.Second, "Timeout")
	cmd.psize = flag.Int("psize", 1024, "Packet size")
	cmd.rate = flag.Float64("rate", 10.0, "Rate in KB/second")
	cmd.reconnect = flag.Bool("reconnect", true, "Re-connect on failures")

	flag.Parse()
	if len(os.Args) < 2 {
		flag.Usage()
		os.Exit(0)
	}

	if *cmd.version {
		fmt.Println(version)
		os.Exit(0)
	}

	if *cmd.isServer {
		os.Exit(cmd.serverMain())
	} else {
		os.Exit(cmd.clientMain())
	}
}

// ----------------------------------------------------------------------
// Client

type ctConn interface {
	Connect(ctx context.Context, address string) error
	Run(ctx context.Context) error
}

type connData struct {
	id               uint32
	psize            int
	rate             float64
	nPacketsSent     uint
	nPacketsReceived uint
	nPacketsDropped  uint
	err              error
	tcpinfo          *tcpinfo.TCPInfo
	started          time.Time
	connected        time.Time
	nReconnect       uint
}

var cData []connData
var nConn uint32

func (c *config) clientMain() int {

	started := time.Now()
	rand.Seed(started.UnixNano())

	// The connection array may contain re-connects
	cData = make([]connData, *c.nconn*10)

	deadline := time.Now().Add(*c.timeout)
	ctx, cancel := context.WithDeadline(context.Background(), deadline)
	defer cancel()

	var wg sync.WaitGroup
	wg.Add(*c.nconn)
	for i := 0; i < *c.nconn; i++ {
		go c.client(ctx, &wg)
	}

	go func (deadline time.Time) {
		for time.Now().Before(deadline) {
			monitorStats()
			time.Sleep(time.Second)
		}
	}(deadline.Add(-time.Second))

	wg.Wait()

	monitorStats()

	return 0

}

func (c *config) client(ctx context.Context, wg *sync.WaitGroup) {
	defer wg.Done()

	for {

		// Check that we have > 2sec until deadline
		deadline, _ := ctx.Deadline()
		if deadline.Sub(time.Now()) < 2*time.Second {
			return
		}

		// Initiate a new connection
		id := atomic.AddUint32(&nConn, 1) - 1
		if int(id) >= len(cData) {
			log.Fatal("Too many re-connects", id)
		}
		cd := &cData[id]
		cd.id = id
		cd.started = time.Now()
		cd.psize = *c.psize
		cd.rate = *c.rate / float64(*c.nconn)

		var conn ctConn
		switch *c.ctype {
		case "fake":
			conn = newFakeConn(cd)
		case "echo":
			conn = newEchoConn(cd)
		default:
			log.Fatal("Unsupported client; ", *c.ctype)
		}

		// Connect with re-try and back-off
		backoff := 100 * time.Millisecond
		cd.err = conn.Connect(ctx, *c.addr)
		for cd.err != nil {
			time.Sleep(backoff)
			if backoff < time.Second {
				backoff += 100 * time.Millisecond
			}
			if deadline.Sub(time.Now()) < 2*time.Second {
				return
			}
			cd.nReconnect++
			cd.err = conn.Connect(ctx, *c.addr)
		}
		cd.connected = time.Now()

		if cd.err = conn.Run(ctx); cd.err == nil {
			return // OK return
		}

		if !*c.reconnect {
			break
		}
	}

}

func newLimiter(ctx context.Context, r float64, psize int) *rate.Limiter {
	// Allow some burstiness but drain the bucket from start
	// Introduce some ramndomness to spread traffic
	lim := rate.NewLimiter(rate.Limit(r*1024.0), psize * 3)
	if lim.WaitN(ctx, rand.Intn(psize)) != nil {
		return nil
	}
	for lim.AllowN(time.Now(), psize) {
	}
	return lim
}

// ----------------------------------------------------------------------
// Fake Connection

type fakeConn struct {
	cd *connData
}

func newFakeConn(cd *connData) ctConn {
	return &fakeConn{
		cd: cd,
	}
}

func (c *fakeConn) Connect(ctx context.Context, address string) error {
	return nil
}

func (c *fakeConn) Run(ctx context.Context) error {
	
	lim := newLimiter(ctx, c.cd.rate, c.cd.psize)
	if lim == nil {
		return nil
	}

	for {
		if lim.WaitN(ctx, c.cd.psize) != nil {
			break
		}
		c.cd.nPacketsSent++
		fmt.Println("Send", c.cd.id)

		// At 10 KB/S and 4 connections a packet should be sent every
		// 400 mS.
		time.Sleep(time.Duration(rand.Intn(440)) * time.Millisecond)
		c.cd.nPacketsReceived++

		if rand.Intn(100) < 5 {
			c.cd.err = fmt.Errorf("HUP")
			return c.cd.err
		}

		for lim.AllowN(time.Now(), c.cd.psize) {
			c.cd.nPacketsDropped++
		}

	}
	return nil
}

// ----------------------------------------------------------------------
// Echo Connection

type echoConn struct {
	cd   *connData
	conn net.Conn
}

func newEchoConn(cd *connData) ctConn {
	return &echoConn{
		cd: cd,
	}
}

func (c *echoConn) Connect(ctx context.Context, address string) error {
	var err error
	c.conn, err = net.DialTimeout("tcp", address, 1500*time.Millisecond)
	return err
}

func (c *echoConn) Run(ctx context.Context) error {
	defer c.conn.Close()

	lim := newLimiter(ctx, c.cd.rate, c.cd.psize)
	if lim == nil {
		return nil
	}

	p := make([]byte, c.cd.psize)
	for {
		if lim.WaitN(ctx, c.cd.psize) != nil {
			break
		}

		if _, err := c.conn.Write(p); err != nil {
			return err
		}
		c.cd.nPacketsSent++

		for lim.AllowN(time.Now(), c.cd.psize) {
			c.cd.nPacketsDropped++
		}

		if err := c.conn.SetReadDeadline(time.Now().Add(time.Second)); err != nil {
			return err
		}
		if _, err := io.ReadFull(c.conn, p); err != nil {
			return err
		}
		c.cd.nPacketsReceived++
	}

	c.cd.tcpinfo, _ = tcpinfo.GetsockoptTCPInfo(&c.conn)
	return nil
}

// ----------------------------------------------------------------------
// Server

func (c *config) serverMain() int {
	l, err := net.Listen("tcp", *c.addr)
	if err != nil {
		log.Fatal(err)
	}
	defer l.Close()

	for {
		conn, err := l.Accept()
		if err != nil {
			log.Fatal(err)
		}
		go server(conn)
	}

	return 0
}

func server(c net.Conn) {
	io.Copy(c, c)
	c.Close()
}

// ----------------------------------------------------------------------
// Statistics

type stats struct {
	Started     time.Time
	Ended       time.Time
	Duration    time.Duration
	Rate        float64
	Connections int
}

func (c *config) reportStats(started time.Time) {
	var nAct, nFail, nPackets, nDropped, nReceived, nReconnect uint
	for _, cd := range cData[:nConn] {
		if cd.err != nil {
			nFail++
		} else {
			nAct++
		}
		nPackets += cd.nPacketsSent
		nDropped += cd.nPacketsDropped
		nReceived += cd.nPacketsReceived
		nReconnect += cd.nReconnect
	}
	fmt.Printf("Conn N/fail/reconnect: %d/%d/%d, Packets send/rec/dropped: %d/%d/%d\n",
		nAct, nFail, nReconnect, nPackets, nReceived, nDropped)
}

func monitorStats() {
	var nAct, nFail, nPackets, nDropped, nReceived, nReconnect uint
	for _, cd := range cData[:nConn] {
		if cd.err != nil {
			nFail++
		} else {
			nAct++
		}
		nPackets += cd.nPacketsSent
		nDropped += cd.nPacketsDropped
		nReceived += cd.nPacketsReceived
		nReconnect += cd.nReconnect
	}
	fmt.Printf("Conn N/fail/reconnect: %d/%d/%d, Packets send/rec/dropped: %d/%d/%d\n",
		nAct, nFail, nReconnect, nPackets, nReceived, nDropped)
}
