// Project page; https://github.com/Nordix/ctraffic/
// LICENSE; MIT. See the "LICENSE" file in the Project page.
// Copyright (c) 2019, Nordix Foundation

package main

import (
	"context"
	"encoding/json"
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

Ctraffic has 3 modes;

 1. Server - simple echo-server
 2. Client - traffic generator
 3. Analyze - Post-analysis of stored statistics

Options;
`

type config struct {
	isServer  *bool
	addr      *string
	nconn     *int
	version   *bool
	timeout   *time.Duration
	monitor   *bool
	psize     *int
	rate      *float64
	reconnect *bool
	ctype     *string
	stats     *string
	statsFile *string
	analyze   *string
}

func main() {
	flag.Usage = func() {
		fmt.Fprintln(flag.CommandLine.Output(), helptext)
		flag.PrintDefaults()
	}

	var cmd config
	cmd.isServer = flag.Bool("server", false, "Act as server")
	cmd.ctype = flag.String("client", "echo", "echo")
	cmd.statsFile = flag.String("stat_file", "", "File for post-test analyzing")
	cmd.addr = flag.String("address", "[::1]:5003", "Server address")
	cmd.nconn = flag.Int("nconn", 1, "Number of connections")
	cmd.version = flag.Bool("version", false, "Print version and quit")
	cmd.timeout = flag.Duration("timeout", 10*time.Second, "Timeout")
	cmd.monitor = flag.Bool("monitor", false, "Monitor")
	cmd.psize = flag.Int("psize", 1024, "Packet size")
	cmd.rate = flag.Float64("rate", 10.0, "Rate in KB/second")
	cmd.reconnect = flag.Bool("reconnect", true, "Re-connect on failures")
	cmd.stats = flag.String("stats", "summary", "none|summary|all")
	cmd.analyze = flag.String("analyze", "throughput", "Post-test analyze")

	flag.Parse()
	if len(os.Args) < 2 {
		flag.Usage()
		os.Exit(0)
	}

	if *cmd.version {
		fmt.Println(version)
		os.Exit(0)
	}

	if *cmd.statsFile != "" {
		os.Exit(cmd.analyzeMain())
	} else if *cmd.isServer {
		os.Exit(cmd.serverMain())
	} else {
		os.Exit(cmd.clientMain())
	}
}

// ----------------------------------------------------------------------
// Analyze

func (c *config) analyzeMain() int {

	// Read statistics
	var err error
	var s *statistics
	if *c.statsFile == "-" {
		s, err = readStats(os.Stdin)
	} else {
		if file, e := os.Open(*c.statsFile); e != nil {
			log.Fatal(e)
		} else {
			s, err = readStats(file)
		}
	}
	if err != nil {
		log.Fatal(err)
	}

	switch *c.analyze {
	case "throughput":
		analyzeThroughput(s)
	case "connections":
		analyzeConnections(s)
	default:
		log.Fatal("Unsupported anayze; ", *c.analyze)
	}
	return 0
}

func analyzeThroughput(s *statistics) {
	if s.Samples == nil {
		log.Fatal("No samples found")
	}
	fmt.Println("Time Throughput")
	last := s.Samples[0]
	for _, samp := range s.Samples[1:] {
		i := samp.Time - last.Time
		// The sample-time is the middle of the interval
		t := last.Time + i/2
		// Throughput is the received/interval in KB/S
		reckb := (samp.Received - last.Received) * s.PacketSize / 1024
		last = samp
		fmt.Println(t.Seconds(), float64(reckb)/i.Seconds())
		last = samp
	}
}

func analyzeConnections(s *statistics) {
	fmt.Println("Time Active New Failed Connecting")
	last := time.Duration(0)
	for i := time.Second; i < s.Duration; i += time.Second {
		var act, fail, connecting, new int
		for _, c := range s.ConnStats {
			if c.Ended == time.Duration(0) {
				log.Fatal("A connection has never ended")
			}
			if c.Ended < last {
				continue
			}
			if c.Ended < i {
				// This connection has ended in our interval
				if c.Err != "" {
					fail++
				}
				continue
			}

			// The remaining connection ends in the future.

			if c.Started > i {
				continue // Not started yet
			}

			if c.Started > last {
				new++ // Started in this interval
			}

			if c.Connect == time.Duration(0) || c.Connect > i {
				connecting++
			} else {
				act++
			}

		}
		imid := last + 500*time.Millisecond
		fmt.Println(imid.Seconds(), act, new, fail, connecting)
		last = i
	}
}

// ----------------------------------------------------------------------
// Client

type ctConn interface {
	Connect(ctx context.Context, address string) error
	Run(ctx context.Context, s *statistics) error
}

// TODO: Use the "connstats" struct in the statistics section
type connData struct {
	id               uint32
	psize            int
	rate             float64
	sent             uint32
	nPacketsReceived uint32
	nPacketsDropped  uint32
	err              error
	tcpinfo          *tcpinfo.TCPInfo
	started          time.Time
	connected        time.Time
	ended            time.Time
	nFailedConnect   uint
}

var cData []connData
var nConn uint32

func (c *config) clientMain() int {

	s := newStats(*c.timeout, *c.rate, *c.nconn, uint32(*c.psize))
	rand.Seed(time.Now().UnixNano())

	// The connection array may contain re-connects
	cData = make([]connData, *c.nconn*10)

	deadline := time.Now().Add(*c.timeout)
	ctx, cancel := context.WithDeadline(context.Background(), deadline)
	defer cancel()

	var wg sync.WaitGroup
	wg.Add(*c.nconn)
	for i := 0; i < *c.nconn; i++ {
		go c.client(ctx, &wg, s)
	}

	if *c.monitor {
		go monitor(s)
	}

	wg.Wait()

	if *c.stats != "none" {
		if *c.stats == "all" {
			s.ConnStats = make([]connstats, nConn)
			for i := range s.ConnStats {
				cs := &s.ConnStats[i]
				cd := &cData[i]
				cs.Started = cd.started.Sub(s.Started)
				cs.Ended = cd.ended.Sub(s.Started)
				if !cd.connected.IsZero() {
					cs.Connect = cd.connected.Sub(s.Started)
				}
				if cd.err != nil {
					cs.Err = cd.err.Error()
				}
				cs.Sent = cd.sent
				cs.Received = cd.nPacketsReceived
				cs.Dropped = cd.nPacketsDropped
				if cd.tcpinfo != nil {
					cs.Retransmits = cd.tcpinfo.Total_retrans
				}
			}
		} else {
			s.Samples = nil
		}
		s.reportStats()
	}

	return 0
}

func (c *config) client(ctx context.Context, wg *sync.WaitGroup, s *statistics) {
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
		case "echo":
			conn = newEchoConn(cd)
		default:
			log.Fatal("Unsupported client; ", *c.ctype)
		}

		// Connect with re-try and back-off
		backoff := 100 * time.Millisecond
		err := conn.Connect(ctx, *c.addr)
		for err != nil {
			time.Sleep(backoff)
			if backoff < time.Second {
				backoff += 100 * time.Millisecond
			}
			if deadline.Sub(time.Now()) < 2*time.Second {
				cd.ended = s.Started.Add(s.Duration)
				return
			}
			cd.nFailedConnect++
			err = conn.Connect(ctx, *c.addr)
		}
		cd.connected = time.Now()

		cd.err = conn.Run(ctx, s)
		if cd.err == nil {
			// NOTE: The connection *will* stop prematurely if the
			// next packet can't be sent before the dead-line. However
			// the stasistics should show that the connection exists
			// to the test end.
			cd.ended = s.Started.Add(s.Duration)
			return // OK return
		}
		cd.ended = time.Now()

		s.failedConnection(1)
		if !*c.reconnect {
			break
		}
	}

}

func monitor(s *statistics) {
	deadline := s.Started.Add(s.Duration - 1500*time.Millisecond)
	for time.Now().Before(deadline) {
		time.Sleep(time.Second)
		var nAct, nConnecting uint
		for _, cd := range cData[:nConn] {
			if cd.err == nil {
				if cd.connected.IsZero() {
					nConnecting++
				} else {
					nAct++
				}
			}
		}
		fmt.Fprintf(
			os.Stderr,
			"Conn act/fail/connecting: %d/%d/%d, Packets send/rec/dropped: %d/%d/%d\n",
			nAct, s.FailedConnections, nConnecting, s.Sent, s.Received, s.Dropped)
	}
}

func newLimiter(ctx context.Context, r float64, psize int) *rate.Limiter {
	// Allow some burstiness but drain the bucket from start
	// Introduce some ramndomness to spread traffic
	lim := rate.NewLimiter(rate.Limit(r*1024.0), psize*10)
	if lim.WaitN(ctx, rand.Intn(psize)) != nil {
		return nil
	}
	for lim.AllowN(time.Now(), psize) {
	}
	return lim
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

func (c *echoConn) Run(ctx context.Context, s *statistics) error {
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
		c.cd.sent++
		s.sent(1)

		for lim.AllowN(time.Now(), c.cd.psize) {
			c.cd.nPacketsDropped++
			s.dropped(1)
		}

		if err := c.conn.SetReadDeadline(time.Now().Add(time.Second)); err != nil {
			return err
		}
		if _, err := io.ReadFull(c.conn, p); err != nil {
			return err
		}
		c.cd.nPacketsReceived++
		s.received(1)
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

type statistics struct {
	Started           time.Time
	Duration          time.Duration
	Rate              float64
	Connections       int
	PacketSize        uint32
	FailedConnections uint32
	Sent              uint32
	Received          uint32
	Dropped           uint32
	Retransmits       uint32
	FailedConnects    uint
	ConnStats         []connstats `json:",omitempty"`
	Samples           []sample    `json:",omitempty"`
}

type connstats struct {
	Started     time.Duration
	Connect     time.Duration
	Ended       time.Duration
	Err         string
	Sent        uint32
	Received    uint32
	Dropped     uint32
	Retransmits uint32
}

type sample struct {
	Time     time.Duration
	Sent     uint32
	Received uint32
	Dropped  uint32
}

func newStats(
	duration time.Duration,
	rate float64,
	connections int,
	packetSize uint32) *statistics {

	s := &statistics{
		Started:     time.Now(),
		Duration:    duration,
		Rate:        rate,
		Connections: connections,
		PacketSize:  packetSize,
		Samples:     make([]sample, 0, duration/time.Second),
	}
	go s.sample()
	return s
}

func (s *statistics) sent(n uint32) {
	atomic.AddUint32(&s.Sent, n)
}
func (s *statistics) received(n uint32) {
	atomic.AddUint32(&s.Received, n)
}
func (s *statistics) dropped(n uint32) {
	atomic.AddUint32(&s.Dropped, n)
}
func (s *statistics) failedConnection(n uint32) {
	atomic.AddUint32(&s.FailedConnections, n)
}

func (s *statistics) reportStats() {
	s.Duration = time.Now().Sub(s.Started)
	json.NewEncoder(os.Stdout).Encode(s)
}

func (s *statistics) sample() {
	deadline := s.Started.Add(s.Duration - 1500*time.Millisecond)
	for time.Now().Before(deadline) {
		time.Sleep(time.Second)
		s.Samples = append(
			s.Samples, sample{time.Now().Sub(s.Started), s.Sent, s.Received, s.Dropped})
	}
}

func readStats(r io.Reader) (*statistics, error) {
	dec := json.NewDecoder(r)
	var s statistics
	if err := dec.Decode(&s); err != nil {
		return nil, err
	}
	return &s, nil
}
