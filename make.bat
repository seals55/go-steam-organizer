@echo off
setlocal

set "BINARY=steam-organizer.exe"
set "COVERAGE_OUT=coverage.out"
set "COVERAGE_HTML=coverage.html"

set "TARGET=%~1"
if "%TARGET%"=="" set "TARGET=all"

if /I "%TARGET%"=="all"      goto :all
if /I "%TARGET%"=="clean"    goto :clean
if /I "%TARGET%"=="build"    goto :build
if /I "%TARGET%"=="test"     goto :test
if /I "%TARGET%"=="coverage" goto :coverage
if /I "%TARGET%"=="vet"      goto :vet
if /I "%TARGET%"=="fmt"      goto :fmt
if /I "%TARGET%"=="tidy"     goto :tidy
if /I "%TARGET%"=="help"     goto :help

echo Unknown target: "%TARGET%"
echo.
goto :help

:all
call :do_clean
if errorlevel 1 exit /b %errorlevel%
call :do_test
if errorlevel 1 exit /b %errorlevel%
call :do_build
exit /b %errorlevel%

:clean
call :do_clean
exit /b %errorlevel%

:build
call :do_build
exit /b %errorlevel%

:test
call :do_test
exit /b %errorlevel%

:coverage
echo ==^> Generating coverage report...
go test -coverprofile="%COVERAGE_OUT%" ./...
if errorlevel 1 exit /b %errorlevel%
go tool cover -html="%COVERAGE_OUT%" -o "%COVERAGE_HTML%"
if errorlevel 1 exit /b %errorlevel%
echo ==^> Coverage report written to %COVERAGE_HTML%
exit /b 0

:vet
echo ==^> Running go vet...
go vet ./...
exit /b %errorlevel%

:fmt
echo ==^> Formatting Go files...
go fmt ./...
exit /b %errorlevel%

:tidy
echo ==^> Tidying Go module dependencies...
go mod tidy
exit /b %errorlevel%

:help
echo Usage: make.bat [target]
echo.
echo Available targets:
echo   all       - Clean, run all tests, and build %BINARY% (default)
echo   clean     - Remove built binaries, coverage files, and test cache
echo   test      - Run unit and integration tests with coverage
echo   coverage  - Generate %COVERAGE_OUT% and %COVERAGE_HTML%
echo   build     - Build %BINARY%
echo   vet       - Run go vet static checks
echo   fmt       - Format Go source code
echo   tidy      - Run go mod tidy
echo   help      - Show this help message
exit /b 0

:do_clean
echo ==^> Cleaning build and test artifacts...
go clean -testcache
if exist "%BINARY%" del /f /q "%BINARY%"
if exist "steam-organizer" del /f /q "steam-organizer"
if exist "%COVERAGE_OUT%" del /f /q "%COVERAGE_OUT%"
if exist "%COVERAGE_HTML%" del /f /q "%COVERAGE_HTML%"
exit /b 0

:do_test
echo ==^> Running tests...
go test -v -cover ./...
exit /b %errorlevel%

:do_build
echo ==^> Building %BINARY%...
go build -v -o "%BINARY%" .
exit /b %errorlevel%

