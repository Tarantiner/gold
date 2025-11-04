@echo off
:: Set Go environment
go env -w GOOS=windows

:: Build check
echo Building ...
go build -ldflags="-s -w -H=windowsgui" -o g.exe
if %errorlevel% neq 0 (
    echo build failed!
    exit /b %errorlevel%
)
upx -9 g.exe

echo All builds completed successfully!
pause