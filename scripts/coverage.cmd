@echo off
setlocal

cd /d "%~dp0../packages/server"
go run github.com/codetreker/go-cov/cmd/go-cov@v0.1.2 %*
set EXITCODE=%ERRORLEVEL%

endlocal & exit /b %EXITCODE%
