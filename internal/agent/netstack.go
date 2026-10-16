package agent

// netstack.go — gvisor userspace TCP/IP stack for the agent (Windows & Linux).

import (
	"context"
	"encoding/binary"
	"fmt"
	"io"
	"log"
	"net"
	"sync"
	"time"

	"golang.org/x/net/icmp"
	xipv4 "golang.org/x/net/ipv4"

	"gvisor.dev/gvisor/pkg/buffer"
	"gvisor.dev/gvisor/pkg/tcpip"
	"gvisor.dev/gvisor/pkg/tcpip/adapters/gonet"
	"gvisor.dev/gvisor/pkg/tcpip/header"
	"gvisor.dev/gvisor/pkg/tcpip/network/ipv4"
	"gvisor.dev/gvisor/pkg/tcpip/network/ipv6"
	"gvisor.dev/gvisor/pkg/tcpip/stack"
	"gvisor.dev/gvisor/pkg/tcpip/transport/tcp"
	"gvisor.dev/gvisor/pkg/tcpip/transport/udp"
	"gvisor.dev/gvisor/pkg/waiter"
)

const (
	nicID  = tcpip.NICID(1)
	mtu    = uint32(1500)
	outCap = 4096
)

type netstack struct {
	s        *stack.Stack
	ep       *tunEndpoint
	icmpConn *icmp.PacketConn
	outChan  chan []byte
	icmpPend sync.Map
	agentIP  net.IP
}

type icmpReply struct {
	srcIP net.IP
	id    int
	seq   int
	data  []byte
}

func newNetstack(agentIP, relayIP net.IP) (*netstack, error) {
	outChan := make(chan []byte, outCap)
	ep := &tunEndpoint{mtu: mtu, outChan: outChan}

	s := stack.New(stack.Options{
		NetworkProtocols: []stack.NetworkProtocolFactory{
			ipv4.NewProtocol,
			ipv6.NewProtocol,
		},
		TransportProtocols: []stack.TransportProtocolFactory{
			tcp.NewProtocol,
			udp.NewProtocol,
		},
		HandleLocal: false,
	})

	// Disable Nagle's algorithm for all connections through this stack.
	tcpDelay := tcpip.TCPDelayEnabled(false)
	s.SetTransportProtocolOption(tcp.ProtocolNumber, &tcpDelay)

	if err := s.CreateNIC(nicID, ep); err != nil {
		return nil, fmt.Errorf("create NIC: %v", err)
	}
	if err := s.SetPromiscuousMode(nicID, true); err != nil {
		return nil, fmt.Errorf("set promiscuous: %v", err)
	}
	if err := s.SetSpoofing(nicID, true); err != nil {
		return nil, fmt.Errorf("set spoofing: %v", err)
	}

	ip4 := tcpip.AddrFrom4([4]byte(agentIP.To4()))
	protoAddr := tcpip.ProtocolAddress{
		Protocol: ipv4.ProtocolNumber,
		AddressWithPrefix: tcpip.AddressWithPrefix{
			Address:   ip4,
			PrefixLen: 30,
		},
	}
	if err := s.AddProtocolAddress(nicID, protoAddr, stack.AddressProperties{}); err != nil {
		return nil, fmt.Errorf("add address: %v", err)
	}

	s.SetRouteTable([]tcpip.Route{
		{Destination: header.IPv4EmptySubnet, NIC: nicID},
		{Destination: header.IPv6EmptySubnet, NIC: nicID},
	})

	ns := &netstack{s: s, ep: ep, outChan: outChan, agentIP: agentIP.To4()}

	tcpFwd := tcp.NewForwarder(s, 256*1024, 65535, ns.handleTCP)
	s.SetTransportProtocolHandler(tcp.ProtocolNumber, tcpFwd.HandlePacket)

	udpFwd := udp.NewForwarder(s, ns.handleUDP)
	s.SetTransportProtocolHandler(udp.ProtocolNumber, udpFwd.HandlePacket)

	ns.initICMP()

	log.Printf("[*] Netstack: virtual IP=%s  relay=%s  ICMP=%v",
		agentIP, relayIP, ns.icmpConn != nil)

	return ns, nil
}

func (ns *netstack) initICMP() {
	c, err := icmp.ListenPacket("ip4:icmp", "0.0.0.0")
	if err != nil {
		log.Printf("[!] ICMP raw socket unavailable (%v) — run as Administrator for ping support", err)
		return
	}
	ns.icmpConn = c
	go ns.icmpReader()
	log.Printf("[*] ICMP raw socket ready (ping support enabled)")
}

func (ns *netstack) icmpReader() {
	buf := make([]byte, 1500)
	for {
		ns.icmpConn.SetReadDeadline(time.Now().Add(1 * time.Second))
		n, src, err := ns.icmpConn.ReadFrom(buf)
		if err != nil {
			if ns.icmpConn == nil {
				return
			}
			continue
		}
		msg, err := icmp.ParseMessage(1, buf[:n])
		if err != nil {
			continue
		}
		if msg.Type != xipv4.ICMPTypeEchoReply {
			continue
		}
		echo, ok := msg.Body.(*icmp.Echo)
		if !ok {
			continue
		}
		key := uint32(echo.ID)<<16 | uint32(echo.Seq)
		if ch, ok := ns.icmpPend.Load(key); ok {
			srcIP := net.ParseIP(src.String())
			if srcIP == nil {
				if ipAddr, ok := src.(*net.IPAddr); ok {
					srcIP = ipAddr.IP
				}
			}
			select {
			case ch.(chan icmpReply) <- icmpReply{
				srcIP: srcIP.To4(),
				id:    echo.ID,
				seq:   echo.Seq,
				data:  echo.Data,
			}:
			default:
			}
		}
	}
}

func (ns *netstack) injectPacket(pkt []byte) error {
	if len(pkt) < 20 {
		return fmt.Errorf("packet too short: %d bytes", len(pkt))
	}
	version := pkt[0] >> 4
	if version == 4 {
		proto := pkt[9]
		if proto == 1 {
			ihl := int(pkt[0]&0x0f) * 4
			if len(pkt) > ihl && pkt[ihl] == 8 {
				dstIP := net.IP(pkt[16:20])
				if dstIP.Equal(ns.agentIP) {
					// kendi virtual IP'mize ping - gvisor cevap versin (fall through)
				} else if ns.icmpConn != nil {
					go ns.forwardICMP(pkt)
					return nil
				} else {
					log.Printf("[!] ICMP skipped (no raw socket): run as Administrator")
					return nil
				}
			}
		}
	}
	var netProto tcpip.NetworkProtocolNumber
	switch version {
	case 4:
		netProto = ipv4.ProtocolNumber
	case 6:
		netProto = ipv6.ProtocolNumber
	default:
		return fmt.Errorf("unknown IP version: %d", version)
	}
	pkb := stack.NewPacketBuffer(stack.PacketBufferOptions{
		Payload: buffer.MakeWithData(pkt),
	})
	defer pkb.DecRef()
	ns.ep.injectInbound(netProto, pkb)
	return nil
}

func (ns *netstack) readOutbound(ctx context.Context) ([]byte, error) {
	select {
	case pkt := <-ns.outChan:
		return pkt, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

func (ns *netstack) close() {
	if ns.icmpConn != nil {
		c := ns.icmpConn
		ns.icmpConn = nil
		c.Close()
	}
	ns.s.Close()
}

func (ns *netstack) handleTCP(r *tcp.ForwarderRequest) {
	id := r.ID()
	dst := fmt.Sprintf("%s:%d", id.LocalAddress.String(), id.LocalPort)
	realConn, err := net.Dial("tcp", dst)
	if err != nil {
		log.Printf("[!] TCP dial %s: %v", dst, err)
		r.Complete(true)
		return
	}
	if tc, ok := realConn.(*net.TCPConn); ok {
		tc.SetNoDelay(true)
	}
	var wq waiter.Queue
	ep, tcpipErr := r.CreateEndpoint(&wq)
	if tcpipErr != nil {
		log.Printf("[!] TCP create endpoint %s: %v", dst, tcpipErr)
		r.Complete(true)
		realConn.Close()
		return
	}
	r.Complete(false)
	virtConn := gonet.NewTCPConn(&wq, ep)
	log.Printf("[→] TCP %s ← %s", dst, id.RemoteAddress.String())
	go func() {
		defer virtConn.Close()
		defer realConn.Close()
		relay2(virtConn, realConn)
	}()
}

func (ns *netstack) handleUDP(r *udp.ForwarderRequest) bool {
	id := r.ID()
	dst := fmt.Sprintf("%s:%d", id.LocalAddress.String(), id.LocalPort)
	var wq waiter.Queue
	ep, tcpipErr := r.CreateEndpoint(&wq)
	if tcpipErr != nil {
		log.Printf("[!] UDP create endpoint %s: %v", dst, tcpipErr)
		return false
	}
	log.Printf("[→] UDP %s ← %s", dst, id.RemoteAddress.String())
	go func() {
		virtConn := gonet.NewUDPConn(&wq, ep)
		defer virtConn.Close()
		realConn, err := net.Dial("udp", dst)
		if err != nil {
			log.Printf("[!] UDP dial %s: %v", dst, err)
			return
		}
		defer realConn.Close()
		relay2(virtConn, realConn)
	}()
	return true
}

func (ns *netstack) forwardICMP(rawPkt []byte) {
	ihl := int(rawPkt[0]&0x0f) * 4
	srcIP := make(net.IP, 4)
	dstIP := make(net.IP, 4)
	copy(srcIP, rawPkt[12:16])
	copy(dstIP, rawPkt[16:20])
	if len(rawPkt) < ihl+8 {
		return
	}
	icmpHdr := rawPkt[ihl:]
	id := binary.BigEndian.Uint16(icmpHdr[4:6])
	seq := binary.BigEndian.Uint16(icmpHdr[6:8])
	payload := make([]byte, len(icmpHdr)-8)
	copy(payload, icmpHdr[8:])
	log.Printf("[→] ICMP echo %s → %s (id=%d seq=%d)", srcIP, dstIP, id, seq)
	key := uint32(id)<<16 | uint32(seq)
	replyCh := make(chan icmpReply, 1)
	ns.icmpPend.Store(key, replyCh)
	defer ns.icmpPend.Delete(key)
	msg := icmp.Message{
		Type: xipv4.ICMPTypeEcho,
		Code: 0,
		Body: &icmp.Echo{ID: int(id), Seq: int(seq), Data: payload},
	}
	b, err := msg.Marshal(nil)
	if err != nil {
		return
	}
	ns.icmpConn.SetWriteDeadline(time.Now().Add(5 * time.Second))
	if _, err := ns.icmpConn.WriteTo(b, &net.IPAddr{IP: dstIP}); err != nil {
		log.Printf("[!] ICMP send → %s: %v", dstIP, err)
		return
	}
	select {
	case reply := <-replyCh:
		replyMsg := icmp.Message{
			Type: xipv4.ICMPTypeEchoReply,
			Code: 0,
			Body: &icmp.Echo{ID: reply.id, Seq: reply.seq, Data: reply.data},
		}
		replyBytes, err := replyMsg.Marshal(nil)
		if err != nil {
			return
		}
		ipPkt := buildIPv4ICMP(reply.srcIP, srcIP, replyBytes)
		select {
		case ns.outChan <- ipPkt:
		default:
		}
		log.Printf("[←] ICMP reply %s ← %s (id=%d seq=%d)", srcIP, dstIP, id, seq)
	case <-time.After(5 * time.Second):
		log.Printf("[!] ICMP timeout %s → %s", srcIP, dstIP)
	}
}

func buildIPv4ICMP(src, dst net.IP, icmpPayload []byte) []byte {
	totalLen := 20 + len(icmpPayload)
	pkt := make([]byte, totalLen)
	pkt[0] = 0x45
	binary.BigEndian.PutUint16(pkt[2:4], uint16(totalLen))
	pkt[8] = 64
	pkt[9] = 1
	copy(pkt[12:16], src.To4())
	copy(pkt[16:20], dst.To4())
	copy(pkt[20:], icmpPayload)
	binary.BigEndian.PutUint16(pkt[10:12], ipv4Checksum(pkt[:20]))
	return pkt
}

func ipv4Checksum(hdr []byte) uint16 {
	var sum uint32
	for i := 0; i+1 < len(hdr); i += 2 {
		sum += uint32(binary.BigEndian.Uint16(hdr[i : i+2]))
	}
	if len(hdr)%2 != 0 {
		sum += uint32(hdr[len(hdr)-1]) << 8
	}
	for sum>>16 != 0 {
		sum = (sum >> 16) + (sum & 0xffff)
	}
	return ^uint16(sum)
}

func relay2(a, b io.ReadWriteCloser) {
	done := make(chan struct{}, 2)
	go func() { io.Copy(a, b); done <- struct{}{} }()
	go func() { io.Copy(b, a); done <- struct{}{} }()
	<-done
}

type tunEndpoint struct {
	dispatcher stack.NetworkDispatcher
	mtu        uint32
	outChan    chan []byte
}

func (e *tunEndpoint) MTU() uint32                                  { return e.mtu }
func (e *tunEndpoint) SetMTU(m uint32)                              { e.mtu = m }
func (e *tunEndpoint) Capabilities() stack.LinkEndpointCapabilities { return 0 }
func (e *tunEndpoint) MaxHeaderLength() uint16                      { return 0 }
func (e *tunEndpoint) LinkAddress() tcpip.LinkAddress               { return "" }
func (e *tunEndpoint) SetLinkAddress(tcpip.LinkAddress)             {}
func (e *tunEndpoint) IsAttached() bool                             { return e.dispatcher != nil }
func (e *tunEndpoint) Wait()                                        {}
func (e *tunEndpoint) ARPHardwareType() header.ARPHardwareType      { return header.ARPHardwareNone }
func (e *tunEndpoint) AddHeader(*stack.PacketBuffer)                {}
func (e *tunEndpoint) ParseHeader(*stack.PacketBuffer) bool         { return true }
func (e *tunEndpoint) Close()                                       {}
func (e *tunEndpoint) SetOnCloseAction(func())                      {}

func (e *tunEndpoint) Attach(d stack.NetworkDispatcher) { e.dispatcher = d }

func (e *tunEndpoint) WritePackets(pkts stack.PacketBufferList) (int, tcpip.Error) {
	n := 0
	for _, pkt := range pkts.AsSlice() {
		var out []byte
		for _, s := range pkt.AsSlices() {
			out = append(out, s...)
		}
		if len(out) == 0 {
			continue
		}
		select {
		case e.outChan <- out:
			n++
		default:
			// Channel full: drop and let gVisor's TCP retransmit.
			// Blocking here would stall gVisor's internal processing (delayed ACKs),
			// which could make the tunnel TCP connection appear unresponsive to the OS.
		}
	}
	return n, nil
}

func (e *tunEndpoint) injectInbound(proto tcpip.NetworkProtocolNumber, pkt *stack.PacketBuffer) {
	if e.dispatcher != nil {
		e.dispatcher.DeliverNetworkPacket(proto, pkt)
	}
}
