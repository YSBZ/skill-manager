package server

import (
	"os"
	"strings"
	"testing"
)

func TestWindowsUpdaterReplacesRuntimeWithExecutable(t *testing.T) {
	source, err := os.ReadFile("update_apply_windows.go")
	if err != nil {
		t.Fatal(err)
	}
	script := string(source)
	for _, required := range []string{
		`set "APP_RUNTIME=!EXE_DIR!runtime"`,
		`set "NEW_RUNTIME=!NEW_DIR!runtime`,
		`set "RUNTIME_STAGE=!EXE_DIR!runtime.update-stage"`,
		`if not exist "!NEW_RUNTIME!\windows-amd64\runtime.json"`,
		`robocopy "!NEW_RUNTIME!" "!RUNTIME_STAGE!" /E /COPY:DAT /R:2 /W:1`,
		`if errorlevel 8 goto stagecopyfail`,
		`if not exist "!RUNTIME_STAGE!\windows-amd64\runtime.json"`,
		`if exist "!RUNTIME_STAGE!" goto stageexists`,
		`if exist "%EXE_BAK%" goto exeBackupexists`,
		`move "!APP_RUNTIME!" "!RUNTIME_BACKUP!"`,
		`move "!RUNTIME_STAGE!" "!APP_RUNTIME!"`,
		`rmdir /s /q "!APP_RUNTIME!"`,
		`set "UPDATE_OK=1"`,
		`if "!UPDATE_OK!"=="1" (`,
	} {
		if !strings.Contains(script, required) {
			t.Errorf("Windows updater is missing runtime replacement step %q", required)
		}
	}
	robocopy := strings.Index(script, `robocopy "!NEW_RUNTIME!" "!RUNTIME_STAGE!"`)
	stageManifest := strings.Index(script, `if not exist "!RUNTIME_STAGE!\windows-amd64\runtime.json"`)
	backupRuntime := strings.Index(script, `move "!APP_RUNTIME!" "!RUNTIME_BACKUP!"`)
	runtimeInstall := strings.Index(script, `move "!RUNTIME_STAGE!" "!APP_RUNTIME!"`)
	executableInstall := strings.LastIndex(script, `copy /y "!NEW!" "%EXE%"`)
	if !(robocopy < stageManifest && stageManifest < backupRuntime && backupRuntime < runtimeInstall && runtimeInstall < executableInstall) {
		t.Error("Windows updater must install the same-volume staged runtime before its executable")
	}
	crossVolumeMove := strings.Index(script, `move "!NEW_RUNTIME!" "!APP_RUNTIME!"`)
	if crossVolumeMove >= 0 {
		t.Error("Windows updater must not move the temporary package runtime directly to the install volume")
	}
}
