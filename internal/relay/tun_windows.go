//go:build windows

package relay

import (
	"fmt"
	"io"
	"log"
	"net"
	"os/exec"
	"sync/atomic"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
	"golang.zx2c4.com/wintun"
)

// ringCapacity, Wintun oturumu için toplam ring kapasitesi (64 MiB — sürücünün
// izin verdiği üst sınır). Yarısı send, yarısı receive ring'e ayrılır; büyük
// kapasite yoğun tarama trafiğindeki kısa süreli yanıt patlamalarını emer ve
// ERROR_BUFFER_OVERFLOW olasılığını ciddi oranda azaltır.
const ringCapacity = 0x4000000

// droppedPackets, ring dolu olduğu için düşürülen paket sayısı (yalnızca teşhis).
var droppedPackets atomic.Uint64

// ---------- Windows API helpers ----------

var (
	modiphlpapi     = windows.NewLazySystemDLL("iphlpapi.dll")
	modkernel32win  = windows.NewLazySystemDLL("kernel32.dll")
	procLuidToAlias = modiphlpapi.NewProc("ConvertInterfaceLuidToAlias")
	procLuidToIndex = modiphlpapi.NewProc("ConvertInterfaceLuidToIndex")
	procWFMO        = modkernel32win.NewProc("WaitForMultipleObjects")
)

func luidToAlias(luid uint64) (string, error) {
	var buf [256]uint16
	ret, _, _ := procLuidToAlias.Call(
		uintptr(unsafe.Pointer(&luid)),
		uintptr(unsafe.Pointer(&buf[0])),
		256,
	)
	if ret != 0 {
		return "", fmt.Errorf("ConvertInterfaceLuidToAlias hata: %d", ret)
	}
	return windows.UTF16ToString(buf[:]), nil
}

func luidToIfIndex(luid uint64) (uint32, error) {
	var idx uint32
	ret, _, _ := procLuidToIndex.Call(
		uintptr(unsafe.Pointer(&luid)),
		uintptr(unsafe.Pointer(&idx)),
	)
	if ret != 0 {
		return 0, fmt.Errorf("ConvertInterfaceLuidToIndex hata: %d", ret)
	}
	return idx, nil
}

// waitForAny blocks until one of handles is signaled.
// Returns the 0-based index of the signaled handle.
func waitForAny(handles []windows.Handle) (int, error) {
	r, _, e := procWFMO.Call(
		uintptr(len(handles)),
		uintptr(unsafe.Pointer(&handles[0])),
		0, // waitAll = false
		uintptr(windows.INFINITE),
	)
	if uint32(r) == 0xffffffff {
		return 0, e
	}
	return int(uint32(r)), nil
}

// ---------- TUN interface ----------

type wintunIface struct {
	adapter    *wintun.Adapter
	session    wintun.Session
	name       string
	closeEvent windows.Handle
}

func (t *wintunIface) Name() string { return t.name }

// Read blocks until a TUN packet arrives or Close is called.
func (t *wintunIface) Read(buf []byte) (int, error) {
	for {
		pkt, err := t.session.ReceivePacket()
		if err == nil {
			n := copy(buf, pkt)
			t.session.ReleaseReceivePacket(pkt)
			return n, nil
		}
		if err != windows.ERROR_NO_MORE_ITEMS {
			return 0, err
		}
		// Ring buffer empty — wait for next packet or close signal.
		handles := []windows.Handle{t.session.ReadWaitEvent(), t.closeEvent}
		idx, werr := waitForAny(handles)
		if werr != nil || idx != 0 {
			return 0, io.EOF
		}
	}
}

// Write sends an IP packet into the TUN adapter.
//
// Yoğun tarama sırasında Wintun send ring'i kısa süreliğine dolabilir;
// WintunAllocateSendPacket bu durumda bloklamaz, ERROR_BUFFER_OVERFLOW döndürür.
// Eskiden bu hata ölümcül kabul edilip tüm tünel kapatılıyordu (5 dakika sonra
// çökmenin asıl tetikleyicisi). Artık kısa bir süre ring'in boşalması beklenir;
// yük sürerse paket sessizce düşürülür — içerideki TCP (gVisor tarafında)
// kendiliğinden yeniden iletir, tünel sağ kalır. Yalnızca gerçek adaptör
// hataları (EOF vb.) hataya dönüştürülür.
func (t *wintunIface) Write(buf []byte) (int, error) {
	const maxOverflowRetries = 15
	for attempt := 0; ; attempt++ {
		pkt, err := t.session.AllocateSendPacket(len(buf))
		if err == nil {
			copy(pkt, buf)
			t.session.SendPacket(pkt)
			return len(buf), nil
		}
		if err != windows.ERROR_BUFFER_OVERFLOW {
			return 0, err
		}
		if attempt >= maxOverflowRetries {
			if n := droppedPackets.Add(1); n == 1 || n%1024 == 0 {
				log.Printf("[!] Wintun send ring dolu: %d paket düşürüldü (tünel korunuyor)", n)
			}
			// Paketi kaybet ama tüneli öldürme.
			return len(buf), nil
		}
		// Ring'in boşalması için kısa bir süre bekle (mikro patlamaları emer).
		time.Sleep(2 * time.Millisecond)
	}
}

// Close signals Read to stop, ends the session, and removes the adapter.
func (t *wintunIface) Close() error {
	windows.SetEvent(t.closeEvent)
	t.session.End()
	windows.CloseHandle(t.closeEvent)
	return t.adapter.Close()
}

// createTUNIface creates a Wintun adapter AND assigns the IP address from cidr.
// Only wintun.dll is required — no OpenVPN / TAP driver needed.
func createTUNIface(name, cidr string) (TUNIface, error) {
	adapter, err := wintun.CreateAdapter(name, "MesutProxy", nil)
	if err != nil {
		return nil, fmt.Errorf(
			"wintun adaptörü oluşturulamadı: %w\n[!] wintun.dll exe'nin yanında mı?", err)
	}

	luid := adapter.LUID()

	actualName, err := luidToAlias(luid)
	if err != nil {
		adapter.Close()
		return nil, fmt.Errorf("wintun adapter adı alınamadı: %w", err)
	}

	ifIdx, err := luidToIfIndex(luid)
	if err != nil {
		adapter.Close()
		return nil, fmt.Errorf("interface index alınamadı: %w", err)
	}

	if err := assignIP(ifIdx, cidr); err != nil {
		adapter.Close()
		return nil, fmt.Errorf("IP atanamadı: %w", err)
	}

	closeEvent, err := windows.CreateEvent(nil, 1, 0, nil)
	if err != nil {
		adapter.Close()
		return nil, fmt.Errorf("close event oluşturulamadı: %w", err)
	}

	session, err := adapter.StartSession(ringCapacity)
	if err != nil {
		windows.CloseHandle(closeEvent)
		adapter.Close()
		return nil, fmt.Errorf("wintun session başlatılamadı: %w", err)
	}

	return &wintunIface{
		adapter:    adapter,
		session:    session,
		name:       actualName,
		closeEvent: closeEvent,
	}, nil
}

// assignIP uses PowerShell to assign an IP to the adapter identified by interface index.
// Using the index avoids any ambiguity caused by duplicate adapter names.
func assignIP(ifIdx uint32, cidr string) error {
	ip, ipNet, err := net.ParseCIDR(cidr)
	if err != nil {
		return err
	}
	prefix, _ := ipNet.Mask.Size()

	// Remove any stale binding for this IP on any interface, then add fresh.
	ps := fmt.Sprintf(
		`Get-NetIPAddress -IPAddress "%s" -ErrorAction SilentlyContinue | `+
			`Remove-NetIPAddress -Confirm:$false -ErrorAction SilentlyContinue; `+
			`New-NetIPAddress -InterfaceIndex %d -IPAddress "%s" -PrefixLength %d -ErrorAction Stop`,
		ip.String(), ifIdx, ip.String(), prefix,
	)
	out, err := exec.Command("powershell", "-NoProfile", "-NonInteractive", "-Command", ps).CombinedOutput()
	if err != nil {
		return fmt.Errorf("powershell: %w — %s", err, out)
	}
	return nil
}
