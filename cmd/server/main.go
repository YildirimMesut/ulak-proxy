package main

import (
	"flag"
	"log"
	"os"
	"runtime"

	"mesut-proxy-win/internal/relay"
)

func main() {
	listen := flag.String("listen", "0.0.0.0:7777", "Dinlenecek adres")
	secret := flag.String("secret", "", "Kimlik doğrulama şifresi (zorunlu)")
	prefix := flag.String("prefix", "mesut", "TUN/TAP arayüz adı ön eki")
	routes := flag.String("routes", "mesut-proxy-routes.json", "Kalıcı route dosyası")
	flag.Parse()

	if *secret == "" {
		log.Println("[!] -secret zorunlu")
		flag.Usage()
		os.Exit(1)
	}

	log.Println("╔══════════════════════════════════════════╗")
	log.Println("║      Mesut-Proxy  — Relay Server         ║")
	log.Println("╚══════════════════════════════════════════╝")
	log.Printf("[*] OS:      %s/%s", runtime.GOOS, runtime.GOARCH)
	log.Printf("[*] Listen:  %s", *listen)
	log.Printf("[*] Prefix:  %s", *prefix)
	log.Printf("[*] Routes:  %s", *routes)

	if runtime.GOOS == "windows" {
		log.Println("[!] Windows: Yönetici (Administrator) olarak çalıştır!")
		log.Println("[!] Windows: wintun.dll exe ile aynı klasörde olmalı")
		log.Println("[!]          https://www.wintun.net/")
	} else {
		log.Println("[!] Linux: root/sudo gerekli (TUN oluşturmak için)")
	}
	log.Println("[i] Konsol için 'help' yazın")
	log.Println()

	r := relay.New(relay.Config{
		ListenAddr: *listen,
		Secret:     *secret,
		TUNPrefix:  *prefix,
		RoutesFile: *routes,
	})

	if err := r.ListenAndServe(); err != nil {
		log.Fatalf("[!] Fatal: %v", err)
	}
}
