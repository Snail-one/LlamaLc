@echo off
setlocal EnableExtensions EnableDelayedExpansion
cd /d "%~dp0.."

if not "%~1"=="" set "VERSION=%~1"
if not defined VERSION set "VERSION=v1.0.0"
set "TARGET_ARCH=%~2"
if not defined TARGET_ARCH set "TARGET_ARCH=amd64"
if /I not "%TARGET_ARCH%"=="amd64" if /I not "%TARGET_ARCH%"=="arm64" (
  echo Error: architecture must be amd64 or arm64
  exit /b 2
)
where go >nul 2>nul || (echo Error: Go 1.22 or later is required & exit /b 1)
for /f "delims=" %%I in ('go list -m') do set "MODULE=%%I"
for /f "delims=" %%I in ('git rev-parse --short HEAD 2^>nul') do set "COMMIT=%%I"
if not defined COMMIT set "COMMIT=unknown"
for /f "delims=" %%I in ('powershell -NoProfile -Command "[DateTime]::UtcNow.ToString('yyyy-MM-ddTHH:mm:ssZ')"') do set "BUILD_DATE=%%I"

set "OUTPUT=dist\windows-!TARGET_ARCH!\LlamaLc\bin"
if not exist "!OUTPUT!" mkdir "!OUTPUT!"
set "GOOS=windows"
set "GOARCH=!TARGET_ARCH!"
set "CGO_ENABLED=0"
for %%P in (llamalc llamaup) do (
  go build -trimpath -ldflags "-s -w -X !MODULE!/internal/version.Version=!VERSION! -X !MODULE!/internal/version.Commit=!COMMIT! -X !MODULE!/internal/version.BuildDate=!BUILD_DATE!" -o "!OUTPUT!\%%P.exe" ".\cmd\%%P" || exit /b 1
)

where go-winres >nul 2>nul
if errorlevel 1 (set "WINRES=go run github.com/tc-hib/go-winres@v0.3.3") else (set "WINRES=go-winres")
for %%P in (llamalc llamaup) do (
  !WINRES! patch --in build\windows\manifest.json --no-backup "!OUTPUT!\%%P.exe" || exit /b 1
)
echo Build complete: %CD%\!OUTPUT!
exit /b 0
