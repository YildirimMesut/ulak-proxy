# Mesut-Proxy

TCP tüneli üzerinden iç ağlara erişim sağlayan hafif bir proxy/tunnel aracı.

---

## Mimari

```
[ Scanner / nxc / nmap ]
         |
  [ SERVER (Relay) ]  ←── dış ağda, herkesçe erişilebilir
    TUN arayüzü
    Route tablosu
         |
    TCP tüneli
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
Dosya, agent'ın erişeceği iç ağ subnet'lerini tanımlar:

```json
{
  "mesut0": [
    "10.10.10.0/24",
    "192.168.1.0/24",
    "172.16.0.0/16"
  ]
}
```

> `mesut0` anahtar adıdır, değiştirilmesine gerek yok.  
> Buraya erişmek istediğin tüm iç ağ subnet'lerini ekle.

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

**Tüm parametreler:**
```
-listen   <ip:port>    Dinlenecek adres         (varsayılan: 0.0.0.0:7777)
-secret   <şifre>      Agent kimlik doğrulaması  (zorunlu)
-prefix   <isim>       TUN arayüz adı öneki      (varsayılan: mesut)
-routes   <dosya>      Route dosyası yolu        (varsayılan: mesut-proxy-routes.json)
```

---

### 3. Agent'ı Başlat

**Windows (iç ağdaki makine):**
```
mesut-proxy-client-windows-amd64.exe -relay SERVER_IP:443 -secret GüçlüŞifre123
```

> ICMP (ping) desteği için **Yönetici (Administrator)** olarak çalıştır.

**Linux (iç ağdaki makine):**
```bash
chmod +x mesut-proxy-client-linux-amd64
sudo ./mesut-proxy-client-linux-amd64 -relay SERVER_IP:443 -secret GüçlüŞifre123
```

**Tüm parametreler:**
```
-relay        <ip:port>    Server adresi           (zorunlu)
-secret       <şifre>      Kimlik doğrulaması       (zorunlu)
-no-reconnect              Otomatik yeniden bağlanmayı kapat
-retry-delay  <saniye>     Yeniden bağlanma bekleme (varsayılan: 5)
-timeout      <saniye>     TCP bağlantı zaman aşımı (varsayılan: 10)
```

---

### 4. Tarama Yap (Server'dan)

Agent bağlandıktan sonra, tarama araçlarını **server terminalinde** çalıştır:

```bash
# SMB tarama
nxc smb 10.10.10.0/24

# Port tarama
nmap -sV 10.10.10.1

# TCP bağlantı testi
nc -zv 10.10.10.1 445
```

---

## Server Konsol Komutları

Server çalışırken ikinci bir terminale gerek yok; komutları doğrudan server penceresine yaz:

```
list                               → Bağlı agent'ları göster
route add <subnet> <agentID>       → Route ekle (10.20.30.0/24 0)
route del <subnet> <agentID>       → Route sil
routes                             → Kayıtlı tüm route'ları listele
forward <agentID> <cPort> <sPort>  → Port yönlendirme aç
unforward <serverPort>             → Port yönlendirmeyi kapat
forwards                           → Aktif port yönlendirmeleri göster
help                               → Bu listeyi tekrar göster
```

**Port yönlendirme örneği:**
```
forward 0 8080 9090
```
→ Server'ın 9090 portuna gelen bağlantılar, agent 0'ın localhost:8080'ine iletilir.

---

## Önemli Notlar

| Konu | Açıklama |
|------|----------|
| **ICMP (ping)** | Agent'ın Administrator/root olarak çalışması gerekir |
| **Tarama yeri** | nxc/nmap **server**'da çalıştırılmalı, agent'ta değil |
| **Çakışan route'lar** | Sunucuda ligolo veya başka bir tunnel varsa, mesut-proxy başlatıldığında o route'ları otomatik olarak devralır (`ip route replace` kullanır) |
| **Çoklu agent** | Birden fazla agent aynı anda bağlanabilir; her biri ayrı bir TUN (`mesut0`, `mesut1`, …) ve `/30` subnet alır |
| **Otomatik yeniden bağlanma** | Agent bağlantı kesilirse varsayılan olarak 5 saniyede bir yeniden bağlanmayı dener |

---

## Derleme (Kaynak Koddan)

**Gereksinimler:** Go 1.21+

```bat
# Windows'ta tüm platformlar için derle:
build.bat
```

Ya da manuel:
```bash
# Linux server
GOOS=linux GOARCH=amd64 go build -o dist/mesut-proxy-server-linux-amd64 ./cmd/server

# Windows server
GOOS=windows GOARCH=amd64 go build -o dist/mesut-proxy-server-windows-amd64.exe ./cmd/server

# Windows agent
GOOS=windows GOARCH=amd64 go build -o dist/mesut-proxy-client-windows-amd64.exe ./cmd/client

# Linux agent
GOOS=linux GOARCH=amd64 go build -o dist/mesut-proxy-client-linux-amd64 ./cmd/client
```

> Windows server binary'si için `wintun.dll`'i [wintun.net](https://www.wintun.net/) adresinden indirip `dist/` klasörüne koy.

---

## dist/ Klasörü İçeriği

```
dist/
├── mesut-proxy-server-linux-amd64        ← Linux server
├── mesut-proxy-server-windows-amd64.exe  ← Windows server
├── mesut-proxy-client-windows-amd64.exe  ← Windows agent
├── mesut-proxy-client-linux-amd64        ← Linux agent
├── mesut-proxy-routes.json               ← Route dosyası (düzenle)
└── wintun.dll                            ← Windows server için gerekli
```
