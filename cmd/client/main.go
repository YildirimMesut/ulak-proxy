package main

import (
	"flag"
	"log"
	"os"
	"time"

	"ulak-proxy/internal/agent"
)

func main() {
	relayAddr    := flag.String("relay",        "",   "Relay adresi (örn. 192.168.1.100:7777) [zorunlu]")
	secret       := flag.String("secret",       "",   "Kimlik doğrulama şifresi [zorunlu]")
	noReconnect  := flag.Bool("no-reconnect",   false, "Otomatik yeniden bağlanmayı devre dışı bırak")
	retryDelay   := flag.Int("retry-delay",     5,    "Yeniden bağlanma bekleme süresi (saniye)")
	dialTimeout  := flag.Int("timeout",         10,   "TCP bağlantı zaman aşımı (saniye)")
	flag.Parse()

	if *relayAddr == "" || *secret == "" {
		log.Println("[!] -relay ve -secret zorunlu")
		flag.Usage()
		os.Exit(1)
	}

	log.Println("╔══════════════════════════════════════════╗")
	log.Println("║          Ulak-Proxy  — Agent             ║")
	log.Println("╚══════════════════════════════════════════╝")
	log.Printf("[*] Relay: %s", *relayAddr)
	log.Printf("[*] Auto-Reconnect: %v (Delay: %ds)", !*noReconnect, *retryDelay)
	log.Println()

	ag := agent.New(agent.Config{
		RelayAddr:      *relayAddr,
		Secret:         *secret,
		NoReconnect:    *noReconnect,
		ReconnectDelay: time.Duration(*retryDelay) * time.Second,
		DialTimeout:    time.Duration(*dialTimeout) * time.Second,
	})

	if err := ag.Run(); err != nil {
		log.Fatalf("[!] Agent stopped: %v", err)
	}
}
