//go:build windows

package server

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
)

// createBreakawayFromJob lets the updater escape any kill-on-close Job Object the
// app (or its WebView2 host) belongs to — without it a helper can be terminated
// the instant the parent exits, which looks like "update said restarting but
// nothing happened". createNoWindow (from runner_windows.go) gives the helper a
// HIDDEN console so the cmd batch's console tools (tasklist/tar/where/copy) work
// while nothing flashes on screen. We deliberately do NOT use DETACHED_PROCESS
// here: a batch driving console utilities needs a console.
const createBreakawayFromJob = 0x01000000

// spawnUpdater writes a detached cmd BATCH that waits for THIS process (pid) to
// exit, extracts the freshly-downloaded, sha256-verified package, swaps the
// installed executable and its complete runtime directory, and relaunches it.
//
// Why a .bat (not PowerShell): on locked-down corporate Windows, Group Policy /
// AppLocker / Constrained Language Mode can refuse to run an unsigned .ps1 even
// with -ExecutionPolicy Bypass — powershell launches but does nothing, so the
// swap silently never happens (observed in the wild: apply.ps1 + download.zip
// present, but no extraction, no relaunch, no log). cmd batch is not subject to
// PowerShell execution policy, and extraction uses the built-in tar.exe (Win10
// 1803+), so no PowerShell at all. Robustness: capped wait (guards PID reuse),
// copy retries (exe can stay briefly locked by WebView2 children / AV), backup +
// rollback, and always relaunch. Everything is logged to <central>/update.log.
func spawnUpdater(zip, installPath string, pid int, centralDir string) error {
	tmp := filepath.Join(centralDir, "update-tmp")
	work := filepath.Join(tmp, "stage")
	script := filepath.Join(tmp, "apply.bat")
	logPath := filepath.Join(centralDir, "update.log")
	body := `@echo off
setlocal enabledelayedexpansion
set "ZIP=%~1"
set "EXE=%~2"
set "PID=%~3"
set "WORK=%~4"
set "LOG=%~5"
set "EXE_BAK=%EXE%.bak"
set "RUNTIME_BACKUP="
set "HAD_RUNTIME=0"
set "RUNTIME_INSTALLED=0"
set "STAGE_CREATED=0"
set "UPDATE_OK=0"
call :log updater start pid=%PID% exe=%EXE%
set /a n=0
:wait
tasklist /fi "PID eq %PID%" /nh 2>nul | find "%PID%" >nul
if errorlevel 1 goto exited
set /a n+=1
if %n% geq 60 goto exited
ping -n 2 127.0.0.1 >nul
goto wait
:exited
call :log waited n=%n% for pid to exit
ping -n 2 127.0.0.1 >nul
rmdir /s /q "%WORK%" 2>nul
mkdir "%WORK%" 2>nul
call :log extracting with tar
tar -xf "%ZIP%" -C "%WORK%" 2>nul
if errorlevel 1 call :log WARN tar returned error
set "NEW="
for /f "delims=" %%F in ('where /r "%WORK%" SkillManager.exe 2^>nul') do if not defined NEW set "NEW=%%F"
if not defined NEW (
  call :log ERROR new exe not found in package; relaunching old
  start "" "%EXE%"
  goto done
)
call :log new exe=!NEW!
for %%F in ("!EXE!") do set "EXE_DIR=%%~dpF"
for %%F in ("!NEW!") do set "NEW_DIR=%%~dpF"
set "APP_RUNTIME=!EXE_DIR!runtime"
set "NEW_RUNTIME=!NEW_DIR!runtime"
set "RUNTIME_STAGE=!EXE_DIR!runtime.update-stage"
set "RUNTIME_BACKUP=!EXE_DIR!runtime.update-backup"
if not exist "!NEW_RUNTIME!\windows-amd64\runtime.json" (
  call :log ERROR package runtime/windows-amd64/runtime.json missing; relaunching old
  start "" "%EXE%"
  goto done
)
if exist "!RUNTIME_STAGE!" goto stageexists
if exist "!RUNTIME_BACKUP!" (
  call :log ERROR runtime rollback directory already exists; relaunching old
  start "" "%EXE%"
  goto done
)
if exist "%EXE_BAK%" goto exeBackupexists
set "STAGE_CREATED=1"
robocopy "!NEW_RUNTIME!" "!RUNTIME_STAGE!" /E /COPY:DAT /R:2 /W:1 /NFL /NDL /NJH /NJS >nul
if errorlevel 8 goto stagecopyfail
if not exist "!RUNTIME_STAGE!\windows-amd64\runtime.json" goto stagemanifestfail
copy /y "%EXE%" "%EXE_BAK%" >nul 2>&1
if errorlevel 1 (
  call :log ERROR could not back up executable; relaunching old
  del /q "%EXE_BAK%" >nul 2>&1
  goto discardstage
)
if exist "!APP_RUNTIME!" (
  move "!APP_RUNTIME!" "!RUNTIME_BACKUP!" >nul 2>&1
  if errorlevel 1 goto runtimebackupfail
  set "HAD_RUNTIME=1"
)
move "!RUNTIME_STAGE!" "!APP_RUNTIME!" >nul 2>&1
if errorlevel 1 goto installfail
set "STAGE_CREATED=0"
set "RUNTIME_INSTALLED=1"
set /a c=0
:copy
copy /y "!NEW!" "%EXE%" >nul 2>&1
if not errorlevel 1 goto copyok
set /a c+=1
call :log copy attempt !c! failed
if !c! geq 10 goto copyfail
ping -n 2 127.0.0.1 >nul
goto copy
:copyfail
call :log ERROR all executable copy attempts failed; rolling back runtime and executable
goto installfail
:runtimebackupfail
call :log ERROR could not move existing runtime; relaunching old executable
del /q "%EXE_BAK%" >nul 2>&1
goto discardstage
:installfail
if "!RUNTIME_INSTALLED!"=="1" (
  rmdir /s /q "!APP_RUNTIME!" 2>nul
  if exist "!APP_RUNTIME!" (
    call :log ERROR could not remove new runtime; prior runtime remains at !RUNTIME_BACKUP!
    goto rollbackexe
  )
)
if "!HAD_RUNTIME!"=="1" (
  move "!RUNTIME_BACKUP!" "!APP_RUNTIME!" >nul 2>&1
  if errorlevel 1 call :log ERROR could not restore prior runtime; backup remains at !RUNTIME_BACKUP!
  if exist "!RUNTIME_BACKUP!" call :log ERROR prior runtime restore incomplete; backup remains at !RUNTIME_BACKUP!
)
:rollbackexe
copy /y "%EXE_BAK%" "%EXE%" >nul 2>&1
if errorlevel 1 (
  call :log ERROR could not restore prior executable; backup remains at %EXE_BAK%
) else (
  del /q "%EXE_BAK%" >nul 2>&1
)
goto discardstage
:copyok
call :log copied new runtime and executable ok
set "UPDATE_OK=1"
:relaunch
if "!UPDATE_OK!"=="1" (
  if exist "%EXE_BAK%" del /q "%EXE_BAK%" >nul 2>&1
  if exist "!RUNTIME_BACKUP!" rmdir /s /q "!RUNTIME_BACKUP!" 2>nul
)
call :log relaunching %EXE%
start "" "%EXE%"
goto done
:stagecopyfail
call :log ERROR robocopy runtime staging failed; removing new staging directory
goto discardstage
:stagemanifestfail
call :log ERROR staged runtime manifest missing; removing new staging directory
goto discardstage
:stageexists
call :log ERROR runtime update staging directory already exists; leaving existing data untouched
goto relaunch
:exeBackupexists
call :log ERROR executable backup already exists; leaving existing data untouched
goto relaunch
:discardstage
if "!STAGE_CREATED!"=="1" if exist "!RUNTIME_STAGE!" (
  rmdir /s /q "!RUNTIME_STAGE!" 2>nul
  if exist "!RUNTIME_STAGE!" call :log ERROR newly created runtime staging remains at !RUNTIME_STAGE!
)
if exist "%EXE_BAK%" (
  call :log ERROR executable backup at %EXE_BAK% remains for recovery
)
call :log relaunching old executable after failed runtime install
start "" "%EXE%"
:done
rmdir /s /q "%WORK%" 2>nul
del /q "%ZIP%" >nul 2>&1
call :log updater done
endlocal
exit /b
:log
echo [%date% %time%] %*>> "%LOG%"
goto :eof
`
	if err := os.WriteFile(script, []byte(body), 0o644); err != nil {
		return err
	}
	args := []string{"/c", script, zip, installPath, fmt.Sprint(pid), work, logPath}
	// Try to break away from any kill-on-close job first; if the job forbids
	// breakaway, CreateProcess fails — fall back to a plain hidden-console launch.
	mk := func(flags uint32) *exec.Cmd {
		c := exec.Command("cmd", args...)
		c.SysProcAttr = &syscall.SysProcAttr{HideWindow: true, CreationFlags: flags}
		return c
	}
	cmd := mk(createNoWindow | createBreakawayFromJob)
	if err := cmd.Start(); err != nil {
		return mk(createNoWindow).Start()
	}
	return nil
}
