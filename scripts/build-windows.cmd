@echo off
setlocal EnableExtensions EnableDelayedExpansion

cd /d "%~dp0.."

if not "%~1"=="" set "VERSION=%~1"
if not defined VERSION (
    for /f "delims=" %%I in ('git describe --tags --always --dirty 2^>nul') do set "VERSION=%%I"
)
if not defined VERSION set "VERSION=dev"

if not defined COMMIT (
    for /f "delims=" %%I in ('git rev-parse --short HEAD 2^>nul') do set "COMMIT=%%I"
)
if not defined COMMIT set "COMMIT=unknown"

if not defined BUILD_DATE (
    for /f "delims=" %%I in ('powershell -NoProfile -Command "[DateTime]::UtcNow.ToString('yyyy-MM-ddTHH:mm:ssZ')"') do set "BUILD_DATE=%%I"
)
if not defined BUILD_DATE set "BUILD_DATE=unknown"

set "TARGET_ARCH=%~2"
if not defined TARGET_ARCH set "TARGET_ARCH=amd64"

if /I not "%TARGET_ARCH%"=="amd64" if /I not "%TARGET_ARCH%"=="arm64" (
    echo Error: architecture must be amd64 or arm64, current: %TARGET_ARCH%
    echo Usage: scripts\build-windows.cmd [version] [amd64^|arm64]
    exit /b 2
)

where go >nul 2>nul
if errorlevel 1 (
    echo Error: Go was not found. Install Go 1.22 or later first.
    exit /b 1
)

if not exist "cmd\llama-launcher" (
    echo Error: cmd\llama-launcher was not found.
    exit /b 1
)
if not exist "cmd\llama-updater" (
    echo Error: cmd\llama-updater was not found.
    exit /b 1
)

for /f "delims=" %%I in ('go list -m') do set "MODULE_PATH=%%I"
if not defined MODULE_PATH (
    echo Error: unable to read module path from go.mod.
    exit /b 1
)

set "OUTPUT_DIR=dist\windows-!TARGET_ARCH!\llama.cpp\bin"
if not exist "!OUTPUT_DIR!" mkdir "!OUTPUT_DIR!"

echo Building Windows !TARGET_ARCH! version !VERSION!...
set "GOOS=windows"
set "GOARCH=!TARGET_ARCH!"
set "CGO_ENABLED=0"

for %%P in (llama-launcher llama-updater) do (
    go build -trimpath -ldflags "-s -w -X !MODULE_PATH!/internal/version.Version=!VERSION! -X !MODULE_PATH!/internal/version.Commit=!COMMIT! -X !MODULE_PATH!/internal/version.BuildDate=!BUILD_DATE!" -o "!OUTPUT_DIR!\%%P.exe" ".\cmd\%%P"
    if errorlevel 1 (
        echo Build failed: %%P
        exit /b 1
    )
)

echo Build complete: %CD%\!OUTPUT_DIR!
echo   llama-launcher.exe
echo   llama-updater.exe
echo Version: !VERSION!
echo Commit: !COMMIT!
echo Build date: !BUILD_DATE!
exit /b 0
