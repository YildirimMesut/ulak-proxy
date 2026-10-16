package agent

import (
	"bufio"
	"context"
	"encoding/binary"
	"fmt"
	"log"
	"net"
	"sync"
	"time"

	"mesut-proxy-win/internal/protocol"
)


type Config struct {
	RelayAddr      string
	Secret         string
	NoReconnect    bool
	ReconnectDelay time.Duration
	DialTimeout    time.Duration
}

type Agent struct {
	cfg Config
}

func New(cfg Config) *Agent {
	if cfg.ReconnectDelay <= 0 {
		cfg.ReconnectDelay = 5 * time.Second
	}
	if cfg.DialTimeout <= 0 {
		cfg.DialTimeout = 10 * time.Second
	}
	return &Agent{cfg: cfg}
}

func (a *Agent) Run() error {
	for {
		err := a.connect()
		if err != nil {
			if a.cfg.NoReconnect {
				return err
			}
			log.Printf("[!] Connection lost: %v", err)
			log.Printf("[*] Reconnecting in %s...", a.cfg.ReconnectDelay)
			time.Sleep(a.cfg.ReconnectDelay)
			continue
		}
		if a.cfg.NoReconnect {
			return nil
		}
		log.Printf("[*] Disconnected. Reconnecting in %s...", a.cfg.ReconnectDelay)
		time.Sleep(a.cfg.ReconnectDelay)
	}
}

func (a *Agent) connect() error {
	log.Printf("[*] Dialing relay at %s...", a.cfg.RelayAddr)

	dialer := &net.Dialer{Timeout: a.cfg.DialTimeout}
	conn, err := dialer.Dial("tcp", a.cfg.RelayAddr)
	if err != nil {
		return fmt.Errorf("dial %s: %w", a.cfg.RelayAddr, err)
	}
	defer conn.Close()
	if tc, ok := conn.(*net.TCPConn); ok {
		tc.SetNoDelay(true)
		tc.SetKeepAlive(true)
		tc.SetKeepAlivePeriod(30 * time.Second)
		tc.SetReadBuffer(4 * 1024 * 1024)
		tc.SetWriteBuffer(4 * 1024 * 1024)
	}

	log.Printf("[+] Connected to relay at %s", a.cfg.RelayAddr)

	if err := protocol.SendAgentHello(conn, a.cfg.Secret); err != nil {
		return fmt.Errorf("send hello: %w", err)
	}

	assignment, err := protocol.ReceiveAuthResponse(conn)
	if err != nil {
		return fmt.Errorf("auth: %w", err)
	}

	log.Printf("[+] Authenticated! Relay=%s  Agent(virtual)=%s",
		assignment.RelayIP, assignment.AgentIP)

	stack, err := newNetstack(assignment.AgentIP, assignment.RelayIP)
	if err != nil {
		return fmt.Errorf("init netstack: %w", err)
	}
	defer stack.close()

	log.Printf("[+] Netstack ready — tunnel is ACTIVE")

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// writeCh feeds the writer goroutine below, eliminating mutex contention
	// when many port-forward goroutines send frames concurrently under heavy load.
	writeCh := make(chan []byte, 512)
	writeFrame := func(frameType byte, payload []byte) error {
		select {
		case writeCh <- protocol.BuildFrame(frameType, payload):
			return nil
		case <-ctx.Done():
			return ctx.Err()
		default:
			return fmt.Errorf("write buffer full")
		}
	}

	var streamMu sync.Mutex
	streams := make(map[uint32]net.Conn)

	errCh := make(chan error, 3)

	// Writer goroutine: sole writer to the tunnel TCP connection.
	var writerWg sync.WaitGroup
	writerWg.Add(1)
	go func() {
		defer writerWg.Done()
		for {
			select {
			case frame := <-writeCh:
				if _, err := conn.Write(frame); err != nil {
					errCh <- fmt.Errorf("relay write: %w", err)
					return
				}
			case <-ctx.Done():
				return
			}
		}
	}()

	// Relay → dispatch
	go func() {
		reader := bufio.NewReaderSize(conn, 256*1024)
		for {
			ft, payload, err := protocol.ReadFrame(reader)
			if err != nil {
				errCh <- fmt.Errorf("relay read: %w", err)
				return
			}
			switch ft {
			case protocol.TypeTUN:
				if err := stack.injectPacket(payload); err != nil {
					log.Printf("[!] Inject error: %v", err)
				}

			case protocol.TypeFwdOpen:
				streamID, port, err := protocol.ParseFwdOpen(payload)
				if err != nil {
					log.Printf("[!] FwdOpen parse: %v", err)
					continue
				}
				go handleFwdOpen(streamID, port, writeFrame, &streams, &streamMu)

			case protocol.TypeFwdData:
				streamID, data, err := protocol.ParseFwdData(payload)
				if err != nil {
					continue
				}
				streamMu.Lock()
				lc, ok := streams[streamID]
				streamMu.Unlock()
				if ok && len(data) > 0 {
					if _, werr := lc.Write(data); werr != nil {
						writeFrame(protocol.TypeFwdClose, uint32ToBytes(streamID))
						streamMu.Lock()
						lc.Close()
						delete(streams, streamID)
						streamMu.Unlock()
					}
				}

			case protocol.TypeFwdClose:
				streamID, err := protocol.ParseFwdClose(payload)
				if err != nil {
					continue
				}
				streamMu.Lock()
				if lc, ok := streams[streamID]; ok {
					lc.Close()
					delete(streams, streamID)
				}
				streamMu.Unlock()
			}
		}
	}()

	// Netstack → Relay: TUN packets dropped if write channel is full
	// (gVisor TCP retransmits; blocking here would stall the whole stack).
	go func() {
		for {
			pkt, err := stack.readOutbound(ctx)
			if err != nil {
				errCh <- fmt.Errorf("stack→relay read: %w", err)
				return
			}
			frame := protocol.BuildFrame(protocol.TypeTUN, pkt)
			select {
			case <-ctx.Done():
				return
			default:
			}
			select {
			case writeCh <- frame:
			default: // drop — gVisor TCP retransmits on its own
			}
		}
	}()

	connErr := <-errCh
	cancel()
	conn.Close()

	// Unblock any handleFwdOpen goroutines blocked on lc.Read.
	streamMu.Lock()
	for _, lc := range streams {
		lc.Close()
	}
	streamMu.Unlock()

	writerWg.Wait()
	return connErr
}

func handleFwdOpen(streamID uint32, port uint16, writeFrame func(byte, []byte) error,
	streams *map[uint32]net.Conn, mu *sync.Mutex) {

	target := fmt.Sprintf("127.0.0.1:%d", port)
	lc, err := net.Dial("tcp", target)
	if err != nil {
		log.Printf("[F] stream %d: dial localhost:%d failed: %v", streamID, port, err)
		writeFrame(protocol.TypeFwdClose, uint32ToBytes(streamID))
		return
	}
	log.Printf("[F] stream %d: connected to localhost:%d", streamID, port)

	mu.Lock()
	(*streams)[streamID] = lc
	mu.Unlock()

	defer func() {
		lc.Close()
		mu.Lock()
		delete(*streams, streamID)
		mu.Unlock()
		writeFrame(protocol.TypeFwdClose, uint32ToBytes(streamID))
		log.Printf("[F] stream %d: closed", streamID)
	}()

	buf := make([]byte, 32768)
	for {
		n, err := lc.Read(buf)
		if n > 0 {
			payload := make([]byte, 4+n)
			binary.BigEndian.PutUint32(payload[0:4], streamID)
			copy(payload[4:], buf[:n])
			if werr := writeFrame(protocol.TypeFwdData, payload); werr != nil {
				return
			}
		}
		if err != nil {
			return
		}
	}
}

func uint32ToBytes(v uint32) []byte {
	b := make([]byte, 4)
	binary.BigEndian.PutUint32(b, v)
	return b
}
