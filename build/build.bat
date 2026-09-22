@echo off
rem Package Windows binaries into build\ :
rem   GXU_Net_AutoLogin.exe       CLI version (console, same behavior as before)
rem   GXU_Net_AutoLogin_Tray.exe  Tray version (GUI subsystem, no console window)
rem
rem Version string comes from git describe and is injected via -X main.version.
rem
rem NOTE: keep this file ASCII-only. cmd.exe reads .bat files with the console
rem codepage (GBK on Chinese Windows), and non-ASCII bytes can swallow line
rem endings - which breaks the script in confusing ways.
setlocal enabledelayedexpansion

rem Script lives in build\, so move up to the repo root
pushd "%~dp0.."

set OUT=build
if not exist "%OUT%" mkdir "%OUT%"

set VERSION=dev
for /f "delims=" %%i in ('git describe --tags --always --dirty 2^>nul') do set VERSION=%%i

rem The tray binary needs tray\rsrc.syso (Common Controls v6 manifest + icon);
rem without it walk cannot even create its main window.
if not exist "tray\rsrc.syso" (
    echo [warn] tray\rsrc.syso is missing, trying to regenerate...
    where rsrc >nul 2>nul
    if errorlevel 1 (
        echo        rsrc not found. Install it with: go install github.com/akavel/rsrc@latest
        echo        Then run, inside tray\ :
        echo        rsrc -arch amd64 -manifest tray.exe.manifest -ico assets/icon.ico -o rsrc.syso
        popd
        exit /b 1
    )
    pushd tray
    rsrc -arch amd64 -manifest tray.exe.manifest -ico assets/icon.ico -o rsrc.syso
    if errorlevel 1 (popd & popd & exit /b 1)
    popd
)

set CGO_ENABLED=0
set GOOS=windows
set GOARCH=amd64

echo [1/2] building CLI version ...
go build -trimpath -ldflags="-s -w -X main.version=%VERSION%" -o "%OUT%\GXU_Net_AutoLogin.exe" .
if errorlevel 1 (popd & exit /b 1)

echo [2/2] building tray version ...
pushd tray
go build -trimpath -ldflags="-s -w -H=windowsgui -X main.version=%VERSION%" -o "..\%OUT%\GXU_Net_AutoLogin_Tray.exe" .
if errorlevel 1 (popd & popd & exit /b 1)
popd

echo.
echo Done. Version: %VERSION%
dir /b "%OUT%\*.exe"
echo.
echo Tray version: put the exe where you want to keep it and double-click.
echo On first run it generates .env next to itself (or uses one in the current
echo directory) and asks for your account and password.
popd
exit /b 0
