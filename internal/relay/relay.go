package relay

import (
	"bufio"
	"context"
	"encoding/binary"
	"fmt"
	"io"
	"log"
	"net"
	"os"
	"os/exec"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"mesut-proxy-win/internal/protocol"
)

type Config struct {
	ListenAddr         string
	Secret             string
	TUNPrefix          string
	TUNBaseSecondOctet int
	RoutesFile         string
}

type portForward struct {
	serverPort uint16
	clientPort uint16
	agentID    uint32
	ln         net.Listener
}

type agentSession struct {
	id      uint32
	tunName string
	relayIP net.IP
	agentIP net.IP
	tun     TUNIface
	conn    net.Conn

	// writeCh feeds the writer goroutine in forward().
	// All callers enqueue pre-serialized frames; the writer goroutine is the
	// only one that calls conn.Write, eliminating mutex contention under heavy load.
	writeCh  chan []byte
	streamMu sync.Mutex
	streams  map[uint32]net.Conn
}

// writeFrame enqueues a frame non-blocking. Returns an error if the channel is full.
func (s *agentSession) writeFrame(frameType byte, payload []byte) error {
	select {
	case s.writeCh <- protocol.BuildFrame(frameType, payload):
		return nil
	default:
		return fmt.Errorf("write buffer full (type=%d)", frameType)
	}
}

type Relay struct {
	cfg      Config
	counter  atomic.Uint32
	streamID atomic.Uint32

	mu     sync.Mutex
	active map[uint32]*agentSession

	fwdMu    sync.Mutex
	forwards map[uint16]*portForward

	routes *routeStore
}

func New(cfg Config) *Relay {
	if cfg.TUNPrefix == "" {
		cfg.TUNPrefix = "mesut"
	}
	if cfg.RoutesFile == "" {
		cfg.RoutesFile = "mesut-proxy-routes.json"
	}
	return &Relay{
		cfg:      cfg,
		active:   make(map[uint32]*agentSession),
		forwards: make(map[uint16]*portForward),
		routes:   newRouteStore(cfg.RoutesFile),
	}
}

func (r *Relay) ListenAndServe() error {
	ln, err := net.Listen("tcp", r.cfg.ListenAddr)
	if err != nil {
		return fmt.Errorf("listen %s: %w", r.cfg.ListenAddr, err)
	}
	defer ln.Close()

	log.Printf("[*] Relay listening on %s (OS: %s)", r.cfg.ListenAddr, runtime.GOOS)
	log.Printf("[*] TUN prefix: %s | Routes: %s", r.cfg.TUNPrefix, r.cfg.RoutesFile)

	go r.runConsole()

	for {
		conn, err := ln.Accept()
		if err != nil {
			log.Printf("[!] Accept: %v", err)
			continue
		}
		go r.handleAgent(conn)
	}
}

func (r *Relay) handleAgent(conn net.Conn) {
	defer conn.Close()
	if tc, ok := conn.(*net.TCPConn); ok {
		tc.SetNoDelay(true)
		tc.SetKeepAlive(true)
		tc.SetKeepAlivePeriod(30 * time.Second)
		tc.SetReadBuffer(4 * 1024 * 1024)
		tc.SetWriteBuffer(4 * 1024 * 1024)
	}
	remote := conn.RemoteAddr().String()
	log.Printf("[+] Incoming from %s", remote)

	if err := protocol.ReceiveAgentHello(conn, r.cfg.Secret); err != nil {
		log.Printf("[!] Auth FAILED %s: %v", remote, err)
		protocol.SendAuthFail(conn)
		return
	}

	id := r.counter.Add(1) - 1
	tunName := fmt.Sprintf("%s%d", r.cfg.TUNPrefix, id)
	octet3 := r.cfg.TUNBaseSecondOctet + int(id)
	relayIP := net.ParseIP(fmt.Sprintf("10.224.%d.1", octet3)).To4()
	agentIP := net.ParseIP(fmt.Sprintf("10.224.%d.2", octet3)).To4()
	tunCIDR := fmt.Sprintf("10.224.%d.1/30", octet3)

	log.Printf("[*] Agent %d: TUN=%s  Relay=%s  Agent=%s", id, tunName, relayIP, agentIP)

	tun, err := createTUNIface(tunName, tunCIDR)
	if err != nil {
		log.Printf("[!] TUN %s: %v", tunName, err)
		protocol.SendAuthFail(conn)
		return
	}
	// On Windows, water opens an existing adapter whose name may differ from tunName.
	// Use the actual interface name for all subsequent netsh / ip commands.
	tunName = tun.Name()
	defer func() {
		tun.Close()
		cleanupTUN(tunName)
		log.Printf("[-] TUN %s removed", tunName)
	}()

	if err := configureTUNCommands(tunName, tunCIDR); err != nil {
		log.Printf("[!] Configure TUN %s: %v", tunName, err)
		protocol.SendAuthFail(conn)
		return
	}

	r.routes.ApplyAll(tunName)

	if err := protocol.SendAuthOK(conn, relayIP, agentIP); err != nil {
		log.Printf("[!] SendAuthOK: %v", err)
		return
	}

	sess := &agentSession{
		id: id, tunName: tunName,
		relayIP: relayIP, agentIP: agentIP,
		tun: tun, conn: conn,
		writeCh: make(chan []byte, 512),
		streams: make(map[uint32]net.Conn),
	}

	r.mu.Lock()
	r.active[id] = sess
	r.mu.Unlock()

	log.Printf("[+] Agent %d UP: TUN=%s (%s) from=%s", id, tunName, tunCIDR, remote)
	log.Printf("[>] Konsol için 'help' yazın")

	r.forward(sess)

	sess.streamMu.Lock()
	for sid, c := range sess.streams {
		c.Close()
		delete(sess.streams, sid)
	}
	sess.streamMu.Unlock()

	r.mu.Lock()
	delete(r.active, id)
	r.mu.Unlock()
	log.Printf("[-] Agent %d disconnected", id)
}

func (r *Relay) forward(sess *agentSession) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Writer goroutine: the sole writer to the tunnel TCP connection.
	// Using a dedicated goroutine avoids mutex contention when many port-forward
	// streams compete to send frames simultaneously under heavy scan load.
	var writerWg sync.WaitGroup
	writerWg.Add(1)
	go func() {
		defer writerWg.Done()
		for {
			select {
			case frame := <-sess.writeCh:
				if _, err := sess.conn.Write(frame); err != nil {
					log.Printf("[!] Agent %d write: %v", sess.id, err)
					cancel()
					sess.conn.Close()
					sess.tun.Close()
					return
				}
			case <-ctx.Done():
				return
			}
		}
	}()

	var wg sync.WaitGroup
	wg.Add(2)

	// TUN → writeCh: TUN packets are dropped if the write channel is full
	// (TCP inside gVisor will retransmit; blocking here would stall gVisor).
	go func() {
		defer wg.Done()
		buf := make([]byte, protocol.MaxPacketSize)
		for {
			n, err := sess.tun.Read(buf)
			if err != nil {
				if err != io.EOF {
					log.Printf("[!] Agent %d TUN read: %v", sess.id, err)
				}
				cancel()
				sess.conn.Close()
				return
			}
			frame := protocol.BuildFrame(protocol.TypeTUN, buf[:n])
			// Non-blocking ctx check, then non-blocking send.
			select {
			case <-ctx.Done():
				return
			default:
			}
			select {
			case sess.writeCh <- frame:
			default: // drop — gVisor TCP retransmits on its own
			}
		}
	}()

	// TCP → dispatch
	go func() {
		defer wg.Done()
		reader := bufio.NewReaderSize(sess.conn, 256*1024)
		for {
			ft, payload, err := protocol.ReadFrame(reader)
			if err != nil {
				if err != io.EOF {
					log.Printf("[!] Agent %d TCP read: %v", sess.id, err)
				}
				cancel()
				sess.tun.Close()
				return
			}
			switch ft {
			case protocol.TypeTUN:
				if _, err := sess.tun.Write(payload); err != nil {
					log.Printf("[!] Agent %d TUN write: %v", sess.id, err)
					cancel()
					sess.tun.Close()
					return
				}
			case protocol.TypeFwdData:
				streamID, data, err := protocol.ParseFwdData(payload)
				if err != nil {
					continue
				}
				sess.streamMu.Lock()
				ec, ok := sess.streams[streamID]
				sess.streamMu.Unlock()
				if ok && len(data) > 0 {
					if _, werr := ec.Write(data); werr != nil {
						sess.writeFrame(protocol.TypeFwdClose, uint32ToBytes(streamID))
						sess.streamMu.Lock()
						ec.Close()
						delete(sess.streams, streamID)
						sess.streamMu.Unlock()
					}
				}
			case protocol.TypeFwdClose:
				streamID, err := protocol.ParseFwdClose(payload)
				if err != nil {
					continue
				}
				sess.streamMu.Lock()
				if c, ok := sess.streams[streamID]; ok {
					c.Close()
					delete(sess.streams, streamID)
				}
				sess.streamMu.Unlock()
			}
		}
	}()

	wg.Wait()
	cancel()
	writerWg.Wait()
}

func (r *Relay) startForward(agentID uint32, clientPort, serverPort uint16) error {
	r.mu.Lock()
	sess, ok := r.active[agentID]
	r.mu.Unlock()
	if !ok {
		return fmt.Errorf("agent %d bağlı değil", agentID)
	}

	r.fwdMu.Lock()
	if _, exists := r.forwards[serverPort]; exists {
		r.fwdMu.Unlock()
		return fmt.Errorf("port %d zaten forward ediliyor", serverPort)
	}
	ln, err := net.Listen("tcp", fmt.Sprintf("0.0.0.0:%d", serverPort))
	if err != nil {
		r.fwdMu.Unlock()
		return fmt.Errorf("listen :%d: %w", serverPort, err)
	}
	pf := &portForward{serverPort: serverPort, clientPort: clientPort, agentID: agentID, ln: ln}
	r.forwards[serverPort] = pf
	r.fwdMu.Unlock()

	log.Printf("[F] Forward 0.0.0.0:%d → agent%d:localhost:%d", serverPort, agentID, clientPort)
	go r.acceptForward(pf, sess)
	return nil
}

func (r *Relay) acceptForward(pf *portForward, sess *agentSession) {
	defer func() {
		pf.ln.Close()
		r.fwdMu.Lock()
		delete(r.forwards, pf.serverPort)
		r.fwdMu.Unlock()
		log.Printf("[F] Forward :%d durduruldu", pf.serverPort)
	}()
	for {
		ec, err := pf.ln.Accept()
		if err != nil {
			return
		}
		sid := r.streamID.Add(1)
		log.Printf("[F] :%d ← %s (stream %d)", pf.serverPort, ec.RemoteAddr(), sid)
		go r.handleForwardStream(sess, sid, pf.clientPort, ec)
	}
}

func (r *Relay) handleForwardStream(sess *agentSession, streamID uint32, clientPort uint16, ec net.Conn) {
	defer ec.Close()

	if err := sess.writeFrame(protocol.TypeFwdOpen, fwdOpenPayload(streamID, clientPort)); err != nil {
		return
	}

	sess.streamMu.Lock()
	sess.streams[streamID] = ec
	sess.streamMu.Unlock()

	buf := make([]byte, 32768)
	for {
		n, err := ec.Read(buf)
		if n > 0 {
			payload := make([]byte, 4+n)
			binary.BigEndian.PutUint32(payload[0:4], streamID)
			copy(payload[4:], buf[:n])
			if werr := sess.writeFrame(protocol.TypeFwdData, payload); werr != nil {
				break
			}
		}
		if err != nil {
			break
		}
	}

	sess.writeFrame(protocol.TypeFwdClose, uint32ToBytes(streamID))
	sess.streamMu.Lock()
	delete(sess.streams, streamID)
	sess.streamMu.Unlock()
}

func (r *Relay) stopForward(serverPort uint16) error {
	r.fwdMu.Lock()
	pf, ok := r.forwards[serverPort]
	r.fwdMu.Unlock()
	if !ok {
		return fmt.Errorf("port %d için aktif forward yok", serverPort)
	}
	pf.ln.Close()
	return nil
}

// ── Platform helpers ──────────────────────────────────────────────────────────

func configureTUNCommands(name, cidr string) error {
	if runtime.GOOS == "windows" {
		// IP assignment is handled inside createTUNIface (Wintun, index-based).
		return nil
	}
	// Linux
	if out, err := exec.Command("ip", "addr", "add", cidr, "dev", name).CombinedOutput(); err != nil {
		return fmt.Errorf("ip addr add: %w — %s", err, out)
	}
	if out, err := exec.Command("ip", "link", "set", name, "up").CombinedOutput(); err != nil {
		return fmt.Errorf("ip link set up: %w — %s", err, out)
	}

	// Enable IP forwarding (required for the relay to route between TUN and real interfaces).
	if out, err := exec.Command("sysctl", "-w", "net.ipv4.ip_forward=1").CombinedOutput(); err != nil {
		log.Printf("[!] sysctl ip_forward: %v — %s", err, out)
	} else {
		log.Printf("[*] Linux: ip_forward enabled")
	}

	// Allow forwarding through the TUN interface in both directions.
	exec.Command("iptables", "-A", "FORWARD", "-i", name, "-j", "ACCEPT").Run()
	exec.Command("iptables", "-A", "FORWARD", "-o", name, "-j", "ACCEPT").Run()
	// Masquerade ALL traffic going into the TUN so return packets are routed back
	// correctly via conntrack — covers both local (nxc on this server) and forwarded
	// (nxc on Windows with routes pointing here) traffic.
	exec.Command("iptables", "-t", "nat", "-A", "POSTROUTING", "-o", name, "-j", "MASQUERADE").Run()
	log.Printf("[*] Linux: iptables FORWARD+MASQUERADE set (iface %s)", name)
	return nil
}

// cleanupTUN removes OS-level state created by configureTUNCommands.
// cidr must match the value passed to configureTUNCommands for the same interface.
func cleanupTUN(name string) {
	if runtime.GOOS != "windows" {
		exec.Command("iptables", "-t", "nat", "-D", "POSTROUTING", "-o", name, "-j", "MASQUERADE").Run()
		exec.Command("iptables", "-D", "FORWARD", "-i", name, "-j", "ACCEPT").Run()
		exec.Command("iptables", "-D", "FORWARD", "-o", name, "-j", "ACCEPT").Run()
		exec.Command("ip", "link", "delete", name).Run()
	}
	// Windows: Wintun adapter is automatically removed when adapter.Close() is called.
}

// ── Console ───────────────────────────────────────────────────────────────────

func (r *Relay) runConsole() {
	scanner := bufio.NewScanner(os.Stdin)
	printHelp()
	for {
		fmt.Print("mesut-proxy> ")
		if !scanner.Scan() {
			return
		}
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		r.handleCommand(strings.Fields(line))
	}
}

func (r *Relay) handleCommand(args []string) {
	switch args[0] {
	case "help", "?":
		printHelp()

	case "list", "ls":
		r.mu.Lock()
		if len(r.active) == 0 {
			fmt.Println("  (bağlı agent yok)")
		}
		for _, s := range r.active {
			fmt.Printf("  agent%-3d  TUN=%-8s  AgentVIP=%s  From=%s  Routes=%v\n",
				s.id, s.tunName, s.agentIP, s.conn.RemoteAddr(), r.routes.List(s.tunName))
		}
		r.mu.Unlock()

	case "route":
		if len(args) < 4 {
			fmt.Println("  Kullanım: route add|del <subnet> <agentID>")
			fmt.Println("  Örnek:    route add 192.168.1.0/24 0")
			return
		}
		agentID, err := strconv.ParseUint(args[3], 10, 32)
		if err != nil {
			fmt.Printf("  [!] Geçersiz agentID: %v\n", err)
			return
		}
		r.mu.Lock()
		sess, ok := r.active[uint32(agentID)]
		r.mu.Unlock()
		if !ok {
			fmt.Printf("  [!] agent %d bağlı değil\n", agentID)
			return
		}
		switch args[1] {
		case "add":
			if err := r.routes.Add(sess.tunName, args[2]); err != nil {
				fmt.Printf("  [!] %v\n", err)
			} else {
				fmt.Printf("  [+] %s via %s kaydedildi ve uygulandı\n", args[2], sess.tunName)
			}
		case "del":
			r.routes.Delete(sess.tunName, args[2])
			fmt.Printf("  [-] %s via %s kaldırıldı\n", args[2], sess.tunName)
		default:
			fmt.Println("  Kullanım: route add|del <subnet> <agentID>")
		}

	case "routes":
		all := r.routes.ListAll()
		if len(all) == 0 {
			fmt.Println("  (kayıtlı route yok)")
			return
		}
		for tun, list := range all {
			for _, s := range list {
				fmt.Printf("  %-10s  %s\n", tun, s)
			}
		}

	case "forward", "fwd":
		if len(args) < 4 {
			fmt.Println("  Kullanım: forward <agentID> <clientPort> <serverPort>")
			fmt.Println("  Örnek:    forward 0 8080 9090")
			fmt.Println("  Anlam:    server:9090'a gelen bağlantılar → agent0:localhost:8080")
			return
		}
		a, e1 := strconv.ParseUint(args[1], 10, 32)
		cp, e2 := strconv.ParseUint(args[2], 10, 16)
		sp, e3 := strconv.ParseUint(args[3], 10, 16)
		if e1 != nil || e2 != nil || e3 != nil {
			fmt.Println("  [!] Geçersiz argümanlar")
			return
		}
		if err := r.startForward(uint32(a), uint16(cp), uint16(sp)); err != nil {
			fmt.Printf("  [!] %v\n", err)
		} else {
			fmt.Printf("  [+] 0.0.0.0:%d → agent%d:localhost:%d\n", sp, a, cp)
		}

	case "unforward", "unfwd":
		if len(args) < 2 {
			fmt.Println("  Kullanım: unforward <serverPort>")
			return
		}
		sp, err := strconv.ParseUint(args[1], 10, 16)
		if err != nil {
			fmt.Printf("  [!] Geçersiz port: %v\n", err)
			return
		}
		if err := r.stopForward(uint16(sp)); err != nil {
			fmt.Printf("  [!] %v\n", err)
		} else {
			fmt.Printf("  [-] Port %d forward durduruldu\n", sp)
		}

	case "forwards", "fwds":
		r.fwdMu.Lock()
		if len(r.forwards) == 0 {
			fmt.Println("  (aktif forward yok)")
		}
		for _, pf := range r.forwards {
			fmt.Printf("  server:%-6d → agent%d:localhost:%d\n", pf.serverPort, pf.agentID, pf.clientPort)
		}
		r.fwdMu.Unlock()

	default:
		fmt.Printf("  Bilinmeyen komut: %s  ('help' yazın)\n", args[0])
	}
}

func printHelp() {
	fmt.Println()
	fmt.Println("  ┌──────────────────────────────────────────────────────────────────┐")
	fmt.Println("  │  Mesut-Proxy Server Konsolu                                      │")
	fmt.Println("  ├──────────────────────────────────────────────────────────────────┤")
	fmt.Println("  │  list                               bağlı agent'lar              │")
	fmt.Println("  │  route add <subnet> <agentID>       kalıcı route ekle            │")
	fmt.Println("  │  route del <subnet> <agentID>       route sil                    │")
	fmt.Println("  │  routes                             kayıtlı route'lar            │")
	fmt.Println("  │  forward <agentID> <cPort> <sPort>  port yönlendir               │")
	fmt.Println("  │  unforward <serverPort>             yönlendirmeyi durdur         │")
	fmt.Println("  │  forwards                           aktif yönlendirmeler         │")
	fmt.Println("  │  help                               bu yardım                    │")
	fmt.Println("  └──────────────────────────────────────────────────────────────────┘")
	fmt.Println()
}

// ── Helpers ───────────────────────────────────────────────────────────────────

func uint32ToBytes(v uint32) []byte {
	b := make([]byte, 4)
	binary.BigEndian.PutUint32(b, v)
	return b
}

func fwdOpenPayload(streamID uint32, port uint16) []byte {
	b := make([]byte, 6)
	binary.BigEndian.PutUint32(b[0:4], streamID)
	binary.BigEndian.PutUint16(b[4:6], port)
	return b
}
