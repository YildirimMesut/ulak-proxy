@echo off
setlocal enabledelayedexpansion

echo ================================================
echo   Mesut-Proxy-Win Build Script
echo   (Windows + Linux Server destegi)
echo ================================================
echo.

set "GOCMD=go"
where go >nul 2>nul
if %ERRORLEVEL% NEQ 0 (
    if exist "C:\Program Files\Go\bin\go.exe" (
        set "GOCMD=C:\Program Files\Go\bin\go.exe"
        set "PATH=C:\Program Files\Go\bin;%PATH%"
    ) else if exist "C:\Go\bin\go.exe" (
        set "GOCMD=C:\Go\bin\go.exe"
        set "PATH=C:\Go\bin;%PATH%"
    ) else (
        echo [HATA] Go bulunamadi! go.dev/dl adresinden yukle.
        pause
        exit /b 1
    )
)

echo [*] Go: "%GOCMD%"
echo.

if not exist dist mkdir dist

echo [1/7] go mod tidy (bagimliliklar indiriliyor)...
"%GOCMD%" mod tidy
if %ERRORLEVEL% NEQ 0 (
    echo [HATA] mod tidy basarisiz
    pause
    exit /b 1
)

echo.
echo [2/7] SERVER: Windows amd64 (TAP-Windows surucusu gerektirir)...
set CGO_ENABLED=0
set GOOS=windows
set GOARCH=amd64
"%GOCMD%" build -ldflags="-s -w" -o dist\mesut-proxy-server-windows-amd64.exe ./cmd/server
if %ERRORLEVEL% EQU 0 (
    echo [+] dist\mesut-proxy-server-windows-amd64.exe
) else (
    echo [HATA] Windows server derlenemedi
    pause
    exit /b 1
)

echo.
echo [3/7] SERVER: Linux amd64...
set CGO_ENABLED=0
set GOOS=linux
set GOARCH=amd64
"%GOCMD%" build -ldflags="-s -w" -o dist\mesut-proxy-server-linux-amd64 ./cmd/server
if %ERRORLEVEL% EQU 0 (
    echo [+] dist\mesut-proxy-server-linux-amd64
) else (
    echo [HATA] Linux server derlenemedi
    pause
    exit /b 1
)

echo.
echo [4/7] SERVER: Linux arm64 (Raspberry Pi vb.)...
set CGO_ENABLED=0
set GOOS=linux
set GOARCH=arm64
"%GOCMD%" build -ldflags="-s -w" -o dist\mesut-proxy-server-linux-arm64 ./cmd/server
if %ERRORLEVEL% EQU 0 (
    echo [+] dist\mesut-proxy-server-linux-arm64
) else (
    echo [UYARI] Linux arm64 server derlenemedi (devam ediliyor)
)

echo.
echo [5/7] CLIENT: Windows amd64...
set CGO_ENABLED=0
set GOOS=windows
set GOARCH=amd64
"%GOCMD%" build -ldflags="-s -w" -o dist\mesut-proxy-client-windows-amd64.exe ./cmd/client
if %ERRORLEVEL% EQU 0 (
    echo [+] dist\mesut-proxy-client-windows-amd64.exe
) else (
    echo [HATA] Windows client derlenemedi
    pause
    exit /b 1
)

echo.
echo [6/7] CLIENT: Linux amd64...
set CGO_ENABLED=0
set GOOS=linux
set GOARCH=amd64
"%GOCMD%" build -ldflags="-s -w" -o dist\mesut-proxy-client-linux-amd64 ./cmd/client
if %ERRORLEVEL% EQU 0 (
    echo [+] dist\mesut-proxy-client-linux-amd64
) else (
    echo [HATA] Linux client derlenemedi
    pause
    exit /b 1
)

echo.
echo [7/7] CLIENT: Linux arm64...
set CGO_ENABLED=0
set GOOS=linux
set GOARCH=arm64
"%GOCMD%" build -ldflags="-s -w" -o dist\mesut-proxy-client-linux-arm64 ./cmd/client
if %ERRORLEVEL% EQU 0 (
    echo [+] dist\mesut-proxy-client-linux-arm64
) else (
    echo [UYARI] Linux arm64 client derlenemedi (devam ediliyor)
)

echo.
echo ================================================
echo TAMAMLANDI! dist\ klasorundeki dosyalar:
echo.
for %%f in (dist\*) do echo   %%f
echo.
echo NOT: Windows server icin TAP-Windows surucusu gerekli.
echo      OpenVPN kurarsan otomatik gelir:
echo      https://openvpn.net/community-downloads/
echo ================================================
pause
