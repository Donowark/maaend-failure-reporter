@echo off
"%~dp0MaaEndFailureReporter.exe" --check --strict
set reporter_exit=%errorlevel%
echo.
echo Reporter exit code: %reporter_exit%
pause
exit /b %reporter_exit%
