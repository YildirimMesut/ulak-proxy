# Ulak-Proxy

**TR** | [EN](#english)

TCP tüneli üzerinden iç ağlara erişim sağlayan hafif bir proxy/tunnel aracı.  
*Ulak: Osmanlıca'da resmi haber ve paket taşıyan kişi.*

---

## Mimari

```
[ Scanner / nxc / nmap ]
         |
  [ SERVER (Relay) ]  ←── dış ağda, herkese açık adres
    TUN arayüzü
    Route tablosu
         |
    TCP tüneli (tek bağlantı)
         |
  [ AGENT (Client) ]  ←── iç ağda, hedeflere erişimi var
    gVisor userspace TCP/IP
         |
  [ Hedef sistemler ]  (10.x.x.x, 192.168.x.x, ...)
```

**Server**, taramanın yapıldığı makinedir.  
**Agent**, iç ağda çalışan ve gerçek TCP bağlantılarını kuran makinedir.

> **Önemli:** `nxc`, `nmap`, `ping` gibi araçlar **SERVER** üzerinden çalıştırılmalıdır.  
> Agent sadece arkaplanda bağlantıyı sürdürür.

---

## Desteklenen Platformlar

| Binary | Platform |
|--------|----------|
| `mesut-proxy-server-linux-amd64` | Linux x64 Server |
| `mesut-proxy-server-windows-amd64.exe` | Windows x64 Server |
| `mesut-proxy-client-windows-amd64.exe` | Windows x64 Agent |
| `mesut-proxy-client-linux-amd64` | Linux x64 Agent |

---

## Hızlı Başlangıç

### 1. Route Dosyasını Hazırla

`mesut-proxy-routes.json` dosyasını server ile **aynı klasöre** koy.  
`mesut-proxy-routes.json.example` dosyasını kopyalayıp düzenleyebilirsin:

```json
{
  "mesut0": [
    "10.10.10.0/24",
    "192.168.1.0/24",
    "172.16.0.0/16"
  ]
}
```

> Erişmek istediğin tüm iç ağ subnet'lerini buraya ekle.

---

### 2. Server'ı Başlat

**Linux (root gerekli):**
```bash
chmod +x mesut-proxy-server-linux-amd64
sudo ./mesut-proxy-server-linux-amd64 -listen 0.0.0.0:443 -secret GüçlüŞifre123
```

**Windows (Administrator olarak çalıştır):**
```
mesut-proxy-server-windows-amd64.exe -listen 0.0.0.0:443 -secret GüçlüŞifre123
```
> Windows server için `wintun.dll` exe ile aynı klasörde olmalıdır.

| Parametre | Açıklama | Varsayılan |
|-----------|----------|------------|
| `-listen` | Dinlenecek adres | `0.0.0.0:7777` |
| `-secret` | Agent kimlik doğrulaması | *zorunlu* |
| `-prefix` | TUN arayüz adı öneki | `mesut` |
| `-routes` | Route dosyası yolu | `mesut-proxy-routes.json` |

---

### 3. Agent'ı Başlat

**Windows (iç ağdaki makine):**
```
mesut-proxy-client-windows-amd64.exe -relay SERVER_IP:443 -secret GüçlüŞifre123
```

**Linux (iç ağdaki makine):**
```bash
chmod +x mesut-proxy-client-linux-amd64
sudo ./mesut-proxy-client-linux-amd64 -relay SERVER_IP:443 -secret GüçlüŞifre123
```

| Parametre | Açıklama | Varsayılan |
|-----------|----------|------------|
| `-relay` | Server adresi | *zorunlu* |
| `-secret` | Kimlik doğrulaması | *zorunlu* |
| `-no-reconnect` | Otomatik yeniden bağlanmayı kapat | `false` |
| `-retry-delay` | Yeniden bağlanma bekleme (sn) | `5` |
| `-timeout` | TCP bağlantı zaman aşımı (sn) | `10` |

---

### 4. Tarama Yap (Server'dan)

Agent bağlandıktan sonra tarama araçlarını **server terminalinde** çalıştır:

```bash
nxc smb 10.10.10.0/24
nmap -sV 10.10.10.1
nc -zv 10.10.10.1 445
```

---

## Server Konsol Komutları

Server çalışırken komutları doğrudan server penceresine yaz:

| Komut | Açıklama |
|-------|----------|
| `list` | Bağlı agent'ları göster |
| `route add <subnet> <agentID>` | Route ekle (`route add 10.20.30.0/24 0`) |
| `route del <subnet> <agentID>` | Route sil |
| `routes` | Kayıtlı tüm route'ları listele |
| `forward <agentID> <cPort> <sPort>` | Port yönlendirme aç |
| `unforward <serverPort>` | Port yönlendirmeyi kapat |
| `forwards` | Aktif port yönlendirmeleri göster |
| `help` | Yardım |

**Port yönlendirme örneği:**
```
forward 0 8080 9090
```
→ Server'ın `:9090` portuna gelen bağlantılar → Agent 0'ın `localhost:8080`'ine iletilir.

---

## Önemli Notlar

| Konu | Açıklama |
|------|----------|
| **ICMP (ping)** | Windows agent'ta yönetici yetkisi gerekmez; Linux agent'ta root gerekli |
| **Tarama yeri** | nxc/nmap server'da çalıştırılmalı, agent'ta değil |
| **Çakışan route'lar** | Ligolo veya başka bir tunnel varsa, ulak-proxy route'ları otomatik devralır |
| **Çoklu agent** | Birden fazla agent aynı anda bağlanabilir; her biri ayrı TUN alır |
| **Otomatik yeniden bağlanma** | Bağlantı kesilirse agent 5 saniyede bir tekrar dener |

---

## Derleme

**Gereksinim:** Go 1.21+

```bat
build.bat
```

Ya da manuel:
```bash
GOOS=linux  GOARCH=amd64 go build -o dist/mesut-proxy-server-linux-amd64       ./cmd/server
GOOS=windows GOARCH=amd64 go build -o dist/mesut-proxy-server-windows-amd64.exe ./cmd/server
GOOS=windows GOARCH=amd64 go build -o dist/mesut-proxy-client-windows-amd64.exe ./cmd/client
GOOS=linux  GOARCH=amd64 go build -o dist/mesut-proxy-client-linux-amd64       ./cmd/client
```

> Windows server için `wintun.dll`'i [wintun.net](https://www.wintun.net/) adresinden indirip `dist/` klasörüne koy.

---

---

<a name="english"></a>

# Ulak-Proxy

**[TR](#ulak-proxy)** | EN

A lightweight TCP tunnel proxy for accessing internal networks.  
*Ulak: Ottoman Turkish word for an official messenger or courier.*

---

## Architecture

```
[ Scanner / nxc / nmap ]
         |
  [ SERVER (Relay) ]  ←── external network, publicly reachable
    TUN interface
    Routing table
         |
    TCP tunnel (single connection)
         |
  [ AGENT (Client) ]  ←── internal network, has access to targets
    gVisor userspace TCP/IP
         |
  [ Target systems ]  (10.x.x.x, 192.168.x.x, ...)
```

The **Server** is the machine where scanning is performed.  
The **Agent** runs inside the target network and establishes real TCP connections.

> **Important:** Tools like `nxc`, `nmap`, and `ping` must be run **on the SERVER**.  
> The agent only maintains the tunnel connection in the background.

---

## Supported Platforms

| Binary | Platform |
|--------|----------|
| `mesut-proxy-server-linux-amd64` | Linux x64 Server |
| `mesut-proxy-server-windows-amd64.exe` | Windows x64 Server |
| `mesut-proxy-client-windows-amd64.exe` | Windows x64 Agent |
| `mesut-proxy-client-linux-amd64` | Linux x64 Agent |

---

## Quick Start

### 1. Prepare the Routes File

Place `mesut-proxy-routes.json` in the **same directory** as the server binary.  
Copy `mesut-proxy-routes.json.example` and edit it:

```json
{
  "mesut0": [
    "10.10.10.0/24",
    "192.168.1.0/24",
    "172.16.0.0/16"
  ]
}
```

> Add all internal subnets you want to reach.

---

### 2. Start the Server

**Linux (requires root):**
```bash
chmod +x mesut-proxy-server-linux-amd64
sudo ./mesut-proxy-server-linux-amd64 -listen 0.0.0.0:443 -secret StrongPassword123
```

**Windows (run as Administrator):**
```
mesut-proxy-server-windows-amd64.exe -listen 0.0.0.0:443 -secret StrongPassword123
```
> Windows server requires `wintun.dll` in the same directory.

| Flag | Description | Default |
|------|-------------|---------|
| `-listen` | Address to listen on | `0.0.0.0:7777` |
| `-secret` | Agent authentication secret | *required* |
| `-prefix` | TUN interface name prefix | `mesut` |
| `-routes` | Routes file path | `mesut-proxy-routes.json` |

---

### 3. Start the Agent

**Windows (machine inside the target network):**
```
mesut-proxy-client-windows-amd64.exe -relay SERVER_IP:443 -secret StrongPassword123
```

**Linux (machine inside the target network):**
```bash
chmod +x mesut-proxy-client-linux-amd64
sudo ./mesut-proxy-client-linux-amd64 -relay SERVER_IP:443 -secret StrongPassword123
```

| Flag | Description | Default |
|------|-------------|---------|
| `-relay` | Server address | *required* |
| `-secret` | Authentication secret | *required* |
| `-no-reconnect` | Disable auto-reconnect | `false` |
| `-retry-delay` | Reconnect delay (seconds) | `5` |
| `-timeout` | TCP dial timeout (seconds) | `10` |

---

### 4. Run Scans (from the Server)

Once the agent connects, run your tools in the **server terminal**:

```bash
nxc smb 10.10.10.0/24
nmap -sV 10.10.10.1
nc -zv 10.10.10.1 445
```

---

## Server Console Commands

Type commands directly into the server window while it is running:

| Command | Description |
|---------|-------------|
| `list` | Show connected agents |
| `route add <subnet> <agentID>` | Add a route (`route add 10.20.30.0/24 0`) |
| `route del <subnet> <agentID>` | Remove a route |
| `routes` | List all saved routes |
| `forward <agentID> <cPort> <sPort>` | Open port forward |
| `unforward <serverPort>` | Close port forward |
| `forwards` | Show active port forwards |
| `help` | Show help |

**Port forwarding example:**
```
forward 0 8080 9090
```
→ Connections to server `:9090` are forwarded to agent 0's `localhost:8080`.

---

## Notes

| Topic | Details |
|-------|---------|
| **ICMP (ping)** | No admin required on Windows agent; Linux agent requires root |
| **Scanning location** | Run nxc/nmap on the server, not the agent |
| **Route conflicts** | If ligolo or another tunnel is present, ulak-proxy automatically overrides its routes |
| **Multiple agents** | Multiple agents can connect simultaneously; each gets its own TUN interface |
| **Auto-reconnect** | Agent retries every 5 seconds on disconnect |

---

## Building from Source

**Requirement:** Go 1.21+

```bat
build.bat
```

Or manually:
```bash
GOOS=linux   GOARCH=amd64 go build -o dist/mesut-proxy-server-linux-amd64       ./cmd/server
GOOS=windows GOARCH=amd64 go build -o dist/mesut-proxy-server-windows-amd64.exe ./cmd/server
GOOS=windows GOARCH=amd64 go build -o dist/mesut-proxy-client-windows-amd64.exe ./cmd/client
GOOS=linux   GOARCH=amd64 go build -o dist/mesut-proxy-client-linux-amd64       ./cmd/client
```

> Download `wintun.dll` from [wintun.net](https://www.wintun.net/) and place it in `dist/` for Windows server support.
