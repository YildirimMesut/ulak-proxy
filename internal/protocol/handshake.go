package protocol

import (
	"crypto/sha256"
	"encoding/binary"
	"fmt"
	"io"
	"net"
)

var Magic = [8]byte{'M', 'E', 'S', 'U', 'T', 'P', 'R', 'X'}

const Version = 0x01

const (
	AuthOK   = 0x00
	AuthFail = 0x01
)

func HashSecret(secret string) [32]byte {
	return sha256.Sum256([]byte(secret))
}

func SendAgentHello(w io.Writer, secret string) error {
	buf := make([]byte, 8+1+32)
	copy(buf[0:8], Magic[:])
	buf[8] = Version
	hash := HashSecret(secret)
	copy(buf[9:41], hash[:])
	_, err := w.Write(buf)
	return err
}

func ReceiveAgentHello(r io.Reader, secret string) error {
	buf := make([]byte, 8+1+32)
	if _, err := io.ReadFull(r, buf); err != nil {
		return fmt.Errorf("read hello: %w", err)
	}
	var magic [8]byte
	copy(magic[:], buf[0:8])
	if magic != Magic {
		return fmt.Errorf("invalid magic bytes")
	}
	if buf[8] != Version {
		return fmt.Errorf("unsupported version: %d", buf[8])
	}
	var gotHash [32]byte
	copy(gotHash[:], buf[9:41])
	if gotHash != HashSecret(secret) {
		return fmt.Errorf("authentication failed: wrong secret")
	}
	return nil
}

func SendAuthOK(w io.Writer, relayIP, agentIP net.IP) error {
	buf := make([]byte, 1+4+4)
	buf[0] = AuthOK
	copy(buf[1:5], relayIP.To4())
	copy(buf[5:9], agentIP.To4())
	_, err := w.Write(buf)
	return err
}

func SendAuthFail(w io.Writer) error {
	_, err := w.Write([]byte{AuthFail})
	return err
}

type IPAssignment struct {
	RelayIP net.IP
	AgentIP net.IP
}

func ReceiveAuthResponse(r io.Reader) (*IPAssignment, error) {
	status := make([]byte, 1)
	if _, err := io.ReadFull(r, status); err != nil {
		return nil, fmt.Errorf("read auth response: %w", err)
	}
	if status[0] != AuthOK {
		return nil, fmt.Errorf("relay rejected authentication")
	}
	ipBuf := make([]byte, 8)
	if _, err := io.ReadFull(r, ipBuf); err != nil {
		return nil, fmt.Errorf("read IP assignment: %w", err)
	}
	relayIP := make(net.IP, 4)
	agentIP := make(net.IP, 4)
	binary.BigEndian.Uint32(ipBuf[0:4])
	copy(relayIP, ipBuf[0:4])
	copy(agentIP, ipBuf[4:8])
	return &IPAssignment{RelayIP: relayIP, AgentIP: agentIP}, nil
}
