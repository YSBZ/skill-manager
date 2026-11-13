package scanner

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func TestInventoryWithIssuesKeepsBrokenLinksWithoutChangingDiscovery(t *testing.T) {
	root := t.TempDir()
	mkSkill(t, root, "good")
	plain := filepath.Join(root, "plain")
	os.MkdirAll(plain, 0o755)
	for name, dest := range map[string]string{"dangling": filepath.Join(root, "gone"), "no-skill": plain, "cycle": filepath.Join(root, "cycle")} {
		if err := os.Symlink(dest, filepath.Join(root, name)); err != nil {
			t.Skipf("symlinks unavailable: %v", err)
		}
	}
	old, err := ScanInventory(root)
	if err != nil || len(old) != 1 || old[0].LinkName != "good" {
		t.Fatalf("discovery must stay unchanged: %+v, %v", old, err)
	}
	got, err := ScanInventoryWithIssues(root)
	if err != nil {
		t.Fatal(err)
	}
	names := []string{}
	for _, sk := range got {
		names = append(names, sk.LinkName)
		if sk.Dir != filepath.Join(root, sk.LogicalName) {
			t.Fatalf("must retain entry path, got %+v", sk)
		}
	}
	if !reflect.DeepEqual(names, []string{"cycle", "dangling", "good", "no-skill"}) {
		t.Fatalf("problem links disappeared or plain folders included: %v", names)
	}
}
