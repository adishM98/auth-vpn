package tunnel

import (
	"os"
	"path/filepath"
	"testing"
)

func TestEnableIPForward(t *testing.T) {
	t.Run("already on: leaves the file alone and reports no error", func(t *testing.T) {
		p := filepath.Join(t.TempDir(), "ip_forward")
		os.WriteFile(p, []byte("1\n"), 0o444) //nolint:errcheck — read-only: any write attempt would fail
		if err := enableIPForward(p); err != nil {
			t.Fatalf("enableIPForward = %v, want nil", err)
		}
	})

	t.Run("off: switches it on by writing the proc file", func(t *testing.T) {
		p := filepath.Join(t.TempDir(), "ip_forward")
		os.WriteFile(p, []byte("0\n"), 0o644) //nolint:errcheck
		if err := enableIPForward(p); err != nil {
			t.Fatalf("enableIPForward = %v, want nil", err)
		}
		if b, _ := os.ReadFile(p); string(b) != "1" {
			t.Errorf("ip_forward = %q, want %q", b, "1")
		}
	})

	t.Run("off and not writable: returns an error", func(t *testing.T) {
		if os.Geteuid() == 0 {
			t.Skip("root ignores file permissions")
		}
		p := filepath.Join(t.TempDir(), "ip_forward")
		os.WriteFile(p, []byte("0\n"), 0o444) //nolint:errcheck
		if err := enableIPForward(p); err == nil {
			t.Fatal("enableIPForward = nil, want error when forwarding is off and can't be enabled")
		}
	})
}
