// Project page; https://github.com/Nordix/ctraffic/
// LICENSE; MIT. See the "LICENSE" file in the Project page.
// Copyright (C) 2025 OpenInfra Foundation Europe. All rights reserved.

package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log"
	"math/rand"
	"net"
	"os"
	"os/signal"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	rndip "github.com/Nordix/mconnect/pkg/rndip/v2"
	tcpinfo "github.com/brucespang/go-tcpinfo"
	"golang.org/x/time/rate"

	"golang.org/x/net/ipv4"
	"golang.org/x/net/ipv6"
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

type addressGenerator interface {
	GetIPStringIdx(cursor uint32) string
}

type config struct {
	isServer  *bool
	addr      *string
	nconn     *int
	retries   *int
	version   *bool
	timeout   *time.Duration
	monitor   *bool
	udp       *bool
	psize     *int
	rate      *float64
	reconnect *bool
	ctype     *string
	stats     *string
	statsFile *string
	analyze   *string
	srccidr   *string
	srcfile   *string
	adrgen    addressGenerator
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
	cmd.retries = flag.Int("retries", 10, "Number of re-connection retries")
	cmd.version = flag.Bool("version", false, "Print version and quit")
	cmd.timeout = flag.Duration("timeout", 10*time.Second, "Timeout")
	cmd.monitor = flag.Bool("monitor", false, "Monitor")
	cmd.psize = flag.Int("psize", 1024, "Packet size")
	cmd.rate = flag.Float64("rate", 10.0, "Rate in KB/second")
	cmd.reconnect = flag.Bool("reconnect", true, "Re-connect on failures")
	cmd.stats = flag.String("stats", "summary", "none|summary|all")
	cmd.analyze = flag.String("analyze", "throughput", "Post-test analyze throughput|hosts|connections")
	cmd.srccidr = flag.String("srccidr", "", "Source CIDR")
	cmd.udp = flag.Bool("udp", false, "Use UDP")
	cmd.srcfile = flag.String("srcfile", "", "Sources from file")

	flag.Parse()
	if len(os.Args) < 2 {
		flag.Usage()
		os.Exit(0)
	}

	if *cmd.version {
		fmt.Println(version)
		os.Exit(0)
	}

	if *cmd.psize < 64 {
		// Must hold a hostname
		*cmd.psize = 64
	}

	if *cmd.statsFile != "" {
		os.Exit(cmd.analyzeMain())
	} else if *cmd.isServer {
		if *cmd.udp {
			go cmd.udpServerMain()
		}
		os.Exit(cmd.serverMain())
	} else {
		if *cmd.udp {
			os.Exit(cmd.udpClientMain())
		}
		os.Exit(cmd.clientMain())
	}
}

type addrPool struct {
	addresses []string
}

func readAddresses(path string) *addrPool {
	// https://golangr.com/read-file/
	file, err := os.Open(path)
	if err != nil {
		log.Fatal(err)
	}
	defer file.Close()

	var lines []string
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		lines = append(lines, scanner.Text())
	}
	return &addrPool{addresses: lines}
}

func (p *addrPool) GetIPStringIdx(cursor uint32) string {
	if int(cursor) < len(p.addresses) {
		adr := p.addresses[cursor]
		return adr
	}
	return ""
}

// Add port ":0" if needed
func withPort(adr string) string {
	if strings.ContainsAny(adr, "[]") {
		if strings.Contains(adr, "]:") {
			return adr
		}
	} else {
		if strings.ContainsAny(adr, ":") {
			return adr
		}
	}
	return fmt.Sprintf("%s:0", adr)
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
	case "hosts":
		analyzeHosts(s)
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
func analyzeHosts(s *statistics) {
	lost := make(map[string]int)
	last := make(map[string]int)
	var nLost, nLast int
	for _, c := range s.ConnStats {
		if c.Host != "" {
			if c.Err == "" {
				nLast++
				last[c.Host]++
			} else {
				nLost++
				lost[c.Host]++
			}
		}
	}
	fmt.Printf("Lost connections: %d\n", nLost)
	printKv(lost)
	fmt.Printf("Lasting connections: %d\n", nLast)
	printKv(last)
}
func printKv(m map[string]int) {
	keys := make([]string, 0)
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, key := range keys {
		fmt.Printf("  %s %d\n", key, m[key])
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
	local            string
	remote           string
	localAddr        net.Addr
	host             string
}

var cData []connData
var nConn uint32

func (c *config) clientMain() int {

	s := newStats(*c.timeout, *c.rate, *c.nconn, uint32(*c.psize))
	rand.Seed(time.Now().UnixNano())

	// The connection array may contain re-connects
	cData = make([]connData, (*c.nconn)*(*c.retries))
	deadline := time.Now().Add(*c.timeout)
	ctx, cancel := context.WithDeadline(context.Background(), deadline)
	defer cancel()
	ctx, cancel = signal.NotifyContext(ctx, syscall.SIGINT, syscall.SIGTERM)
	defer cancel()

	if *c.srccidr != "" {
		var err error
		c.adrgen, err = rndip.New(*c.srccidr)
		if err != nil {
			log.Fatal("Set source failed:", err)
		}
	} else if *c.srcfile != "" {
		c.adrgen = readAddresses(*c.srcfile)
	}

	var wg sync.WaitGroup
	wg.Add(*c.nconn)
	for i := 0; i < *c.nconn; i++ {
		go c.client(ctx, &wg, s)
	}

	if *c.monitor {
		go monitor(s)
	}

	wg.Wait()

	c.printStats(s)
	return 0
}

func (c *config) printStats(s *statistics) {
	if *c.stats != "none" {
		c.copyStats(s)
		s.reportStats()
	}
}

func (c *config) copyStats(s *statistics) {
	if *c.stats == "all" {
		s.ConnStats = make([]connstats, nConn)
		for i := 0; len(cData) > i && len(s.ConnStats) > i; i++ {
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
				s.Retransmits += cd.tcpinfo.Total_retrans
			}
			cs.Local = cd.local
			cs.Remote = cd.remote
			cs.Host = cd.host
		}
	} else {
		var i uint32
		for i = 0; uint32(len(cData)) > i; i++ {
			cd := &cData[i]
			if cd.tcpinfo != nil {
				s.Retransmits += cd.tcpinfo.Total_retrans
			}
		}
		s.Samples = nil
	}
}

func (c *config) client(ctx context.Context, wg *sync.WaitGroup, s *statistics) {
	defer wg.Done()

	for {

		// Check that we have > 2sec until deadline
		deadline, _ := ctx.Deadline()
		if time.Until(deadline) < 2*time.Second {
			return
		}

		// Initiate a new connection
		id := atomic.AddUint32(&nConn, 1) - 1
		if int(id) >= len(cData) {
			c.printStats(s)
			log.Fatal("Too many re-connects: ", id)
		}
		cd := &cData[id]
		cd.id = id
		cd.started = time.Now()
		cd.psize = *c.psize
		cd.rate = *c.rate / float64(*c.nconn)
		if c.adrgen != nil {
			a := c.adrgen.GetIPStringIdx(id)
			if a == "" {
				log.Fatalln("Ran out of source addresses")
			}
			sadr := withPort(a)
			if saddr, err := net.ResolveTCPAddr("tcp", sadr); err != nil {
				log.Fatal(err)
			} else {
				cd.localAddr = saddr
			}
		}

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
			if ctx.Err() != nil {
				// Interrupt or timeout
				cd.ended = s.Started.Add(s.Duration)
				s.failedConnect(1)
				return
			}
			if backoff < time.Second {
				backoff += 100 * time.Millisecond
			}
			if time.Until(deadline) < 2*time.Second {
				cd.ended = s.Started.Add(s.Duration)
				return
			}
			s.failedConnect(1)
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
		monConns := uint32(len(cData))
		if monConns > nConn {
			monConns = nConn
		}
		for _, cd := range cData[:monConns] {
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

	d := net.Dialer{
		LocalAddr: c.cd.localAddr,
		Timeout:   1500 * time.Millisecond,
	}
	c.conn, err = d.DialContext(ctx, "tcp", address)
	return err
}

func (c *echoConn) Run(ctx context.Context, s *statistics) error {
	defer c.conn.Close()

	c.cd.local = c.conn.LocalAddr().String()
	c.cd.remote = c.conn.RemoteAddr().String()

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
		if c.cd.nPacketsReceived == 0 {
			// First received packet _may_ contain a hostname
			if n := bytes.IndexByte(p, 0); n > 0 {
				c.cd.host = string(p[:n])
			}
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
	log.Println("Listen on address; ", *c.addr)

	for {
		conn, err := l.Accept()
		if err != nil {
			log.Fatal(err)
		}
		go server(conn)
	}
}

func server(c net.Conn) {
	defer c.Close()

	// Insert our hostname in the first packet
	p := make([]byte, 64)
	if _, err := io.ReadFull(c, p); err != nil {
		return
	}
	if host, err := os.Hostname(); err == nil {
		copy(p[:], host)
	}
	if _, err := c.Write(p); err != nil {
		return
	}

	io.Copy(c, c)
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
	FailedConnects    uint32
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
	Local       string
	Remote      string
	Host        string `json:",omitempty"`
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
func (s *statistics) failedConnect(n uint32) {
	atomic.AddUint32(&s.FailedConnects, n)
}

func (s *statistics) reportStats() {
	s.Duration = time.Since(s.Started)
	json.NewEncoder(os.Stdout).Encode(s)
}

func (s *statistics) sample() {
	deadline := s.Started.Add(s.Duration - 1500*time.Millisecond)
	for time.Now().Before(deadline) {
		time.Sleep(time.Second)
		s.Samples = append(
			s.Samples, sample{time.Since(s.Started), s.Sent, s.Received, s.Dropped})
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

// ----------------------------------------------------------------------
// UDP

func (c *config) udpServerMain() int {
	serverAddr, err := net.ResolveUDPAddr("udp", *c.addr)
	if err != nil {
		log.Fatal(err)
	}
	conn, err := net.ListenUDP("udp", serverAddr)
	if err != nil {
		log.Fatal(err)
	}
	log.Println("Listen on UDP address; ", *c.addr)

	if err := setUDPSocketOptions(conn); err != nil {
		log.Fatal(err)
	}

	host, err := os.Hostname()
	if err != nil {
		host = ""
	}

	buf := make([]byte, 64*1024)
	oob := make([]byte, 2048)
	for {
		//n, oobn, flags, addr, err
		n, oobn, _, addr, err := conn.ReadMsgUDP(buf, oob)
		if err != nil {
			log.Fatal(err)
		}
		oobd := oob[:oobn]

		copy(buf[:], host)

		_, _, err = conn.WriteMsgUDP(buf[:n], correctSource(oobd), addr)
		if err != nil {
			log.Fatal(err)
		}
	}
}

func (c *config) udpClientMain() int {
	s := newStats(*c.timeout, *c.rate, *c.nconn, uint32(*c.psize))
	rand.Seed(time.Now().UnixNano())

	// The connection array will not contain re-connects for UDP
	cData = make([]connData, *c.nconn)

	deadline := time.Now().Add(*c.timeout)
	ctx, cancel := context.WithDeadline(context.Background(), deadline)
	defer cancel()

	if *c.srccidr != "" {
		var err error
		c.adrgen, err = rndip.New(*c.srccidr)
		if err != nil {
			log.Fatal("Set source failed:", err)
		}
	} else if *c.srcfile != "" {
		c.adrgen = readAddresses(*c.srcfile)
	}

	var wg sync.WaitGroup
	wg.Add(*c.nconn)
	for i := 0; i < *c.nconn; i++ {
		go c.udpClient(ctx, &wg, s)
	}

	if *c.monitor {
		go monitor(s)
	}

	wg.Wait()

	c.printStats(s)

	return 0
}

type udpConn struct {
	cd   *connData
	conn *net.UDPConn
}

func (c *config) udpClient(
	ctx context.Context, wg *sync.WaitGroup, s *statistics) {
	defer wg.Done()

	for {

		// Check that we have > 1sec until deadline
		deadline, _ := ctx.Deadline()
		if time.Until(deadline) < 1*time.Second {
			return
		}

		// Initiate a new connection
		id := atomic.AddUint32(&nConn, 1) - 1
		if int(id) >= len(cData) {
			c.printStats(s)
			log.Fatal("Too many re-connects: ", id)
		}
		cd := &cData[id]
		cd.id = id
		cd.started = time.Now()
		cd.psize = *c.psize
		cd.rate = *c.rate / float64(*c.nconn)
		var saddr *net.UDPAddr
		if c.adrgen != nil {
			var err error
			a := c.adrgen.GetIPStringIdx(id)
			if a == "" {
				log.Fatalln("Ran out of source addresses")
			}
			sadr := withPort(a)
			if saddr, err = net.ResolveUDPAddr("udp", sadr); err != nil {
				log.Fatal(err)
			} else {
				cd.localAddr = saddr
			}
		}

		daddr, err := net.ResolveUDPAddr("udp", *c.addr)
		if err != nil {
			log.Fatal(err)
		}

		conn, err := net.DialUDP("udp", saddr, daddr)
		if err != nil {
			log.Fatal(err)
		}
		defer conn.Close()
		cd.connected = time.Now()

		udpConn := udpConn{cd, conn}
		cd.err = udpConn.Run(ctx, s)
		if cd.err == nil {
			// NOTE: The connection *will* stop prematurely if the
			// next packet can't be sent before the dead-line. However
			// the stasistics should show that the connection exists
			// to the test end.
			cd.ended = s.Started.Add(s.Duration)
			return // OK return
		}
		cd.ended = time.Now()
	}
}

func (c *udpConn) Run(ctx context.Context, s *statistics) error {
	defer c.conn.Close()

	c.cd.local = c.conn.LocalAddr().String()
	c.cd.remote = c.conn.RemoteAddr().String()

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
		_, _, err := c.conn.ReadFrom(p)
		if err != nil {
			// Probably a timeout, i.e. a lost packet
			continue
		}

		if c.cd.nPacketsReceived == 0 {
			// First received packet _may_ contain a hostname
			if n := bytes.IndexByte(p, 0); n > 0 {
				c.cd.host = string(p[:n])
			}
		}

		c.cd.nPacketsReceived++
		s.received(1)
	}
	return nil
}

/*
  Taken from;
   https://github.com/miekg/dns/blob/master/udp.go
  License;
   https://github.com/miekg/dns/blob/master/LICENSE
*/

func setUDPSocketOptions(conn *net.UDPConn) error {
	// Try setting the flags for both families and ignore the errors unless they
	// both error.
	err6 := ipv6.NewPacketConn(conn).SetControlMessage(ipv6.FlagDst|ipv6.FlagInterface, true)
	err4 := ipv4.NewPacketConn(conn).SetControlMessage(ipv4.FlagDst|ipv4.FlagInterface, true)
	if err6 != nil && err4 != nil {
		return err4
	}
	return nil
}

// parseDstFromOOB takes oob data and returns the destination IP.
func parseDstFromOOB(oob []byte) net.IP {
	// Start with IPv6 and then fallback to IPv4
	// TODO(fastest963): Figure out a way to prefer one or the other. Looking at
	// the lvl of the header for a 0 or 41 isn't cross-platform.
	cm6 := new(ipv6.ControlMessage)
	if cm6.Parse(oob) == nil && cm6.Dst != nil {
		return cm6.Dst
	}
	cm4 := new(ipv4.ControlMessage)
	if cm4.Parse(oob) == nil && cm4.Dst != nil {
		return cm4.Dst
	}
	return nil
}

// correctSource takes oob data and returns new oob data with the Src equal to the Dst
func correctSource(oob []byte) []byte {
	dst := parseDstFromOOB(oob)
	if dst == nil {
		return nil
	}
	// If the dst is definitely an IPv6, then use ipv6's ControlMessage to
	// respond otherwise use ipv4's because ipv6's marshal ignores ipv4
	// addresses.
	if dst.To4() == nil {
		cm := new(ipv6.ControlMessage)
		cm.Src = dst
		oob = cm.Marshal()
	} else {
		cm := new(ipv4.ControlMessage)
		cm.Src = dst
		oob = cm.Marshal()
	}
	return oob
}
