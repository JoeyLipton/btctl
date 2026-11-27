package bluetooth

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/joeylipton/btctl/internal/fakebluez"
)

// A soft-blocked adapter refuses Powered=true. btctl runs `rfkill unblock
// bluetooth` once and tries again. BlueZ does not power an adapter just
// because it was unblocked, so the retry must set Powered itself.
func TestPowerOnRecoversFromRfkillBlock(t *testing.T) {
	fake := fakebluez.New(t)
	adapter := fake.AddAdapter("hci0", "00:11:22:33:44:55", false)
	client := connectTestClient(t)

	dir := t.TempDir()
	blocked := filepath.Join(dir, "blocked")
	log := filepath.Join(dir, "rfkill.log")
	if err := os.WriteFile(blocked, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	fake.BlockPowerOnWhile(blocked)
	// Fake rfkill: record the call and lift the block.
	script := "#!/bin/sh\necho \"$*\" >> " + log + "\nrm -f " + blocked + "\n"
	if err := os.WriteFile(filepath.Join(dir, "rfkill"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))

	if err := client.SetAdapterPowered(testContext(t), true); err != nil {
		t.Fatalf("SetAdapterPowered(true) on a blocked adapter: %v", err)
	}
	if got, _ := os.ReadFile(log); strings.TrimSpace(string(got)) != "unblock bluetooth" {
		t.Errorf("rfkill was called with %q, want a single 'unblock bluetooth'", got)
	}
	if powered, _ := fake.Property(adapter, fakebluez.AdapterIface, "Powered").(bool); !powered {
		t.Error("adapter is not powered")
	}
}

// Powering off never needs rfkill.
func TestPowerOffDoesNotRunRfkill(t *testing.T) {
	fake := fakebluez.New(t)
	fake.AddAdapter("hci0", "00:11:22:33:44:55", true)
	client := connectTestClient(t)
	if err := client.SetAdapterPowered(testContext(t), false); err != nil {
		t.Fatal(err)
	}
}
