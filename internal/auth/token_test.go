package auth

import (
	"os"
	"path/filepath"
	"testing"
)

func TestTokenManager(t *testing.T) {
	t.Run("a revoke from another process is seen by the running server", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "tokens.yaml")
		srv, _ := NewManager(path)
		raw, err := srv.Add("alice", nil, false)
		if err != nil {
			t.Fatal(err)
		}

		cli, _ := NewManager(path) // the `server tokens revoke` process
		if err := cli.Revoke("alice"); err != nil {
			t.Fatal(err)
		}

		if _, err := srv.Validate(raw); err == nil {
			t.Fatal("server still accepts a token revoked by the CLI")
		}
	})

	t.Run("a later server save does not bring a revoked token back", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "tokens.yaml")
		srv, _ := NewManager(path)
		raw, _ := srv.Add("alice", nil, false)
		cli, _ := NewManager(path)
		_ = cli.Revoke("alice")

		if _, err := srv.Add("bob", nil, false); err != nil {
			t.Fatal(err)
		}

		fresh, _ := NewManager(path)
		if _, err := fresh.Validate(raw); err == nil {
			t.Fatal("revoked token was written back to disk")
		}
	})

	t.Run("names are unique", func(t *testing.T) {
		m, _ := NewManager(filepath.Join(t.TempDir(), "tokens.yaml"))
		if _, err := m.Add("alice", nil, false); err != nil {
			t.Fatal(err)
		}
		if _, err := m.Add("alice", nil, false); err == nil {
			t.Fatal("second token with the same name was accepted")
		}
	})

	t.Run("revoke removes every legacy duplicate", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "tokens.yaml")
		dup := "tokens:\n- name: alice\n  hash: " + hashToken("one") + "\n- name: alice\n  hash: " + hashToken("two") + "\n"
		if err := os.WriteFile(path, []byte(dup), 0o600); err != nil {
			t.Fatal(err)
		}
		m, _ := NewManager(path)
		if err := m.Revoke("alice"); err != nil {
			t.Fatal(err)
		}
		for _, raw := range []string{"one", "two"} {
			if _, err := m.Validate(raw); err == nil {
				t.Fatalf("token %q survived revoke", raw)
			}
		}
	})

	t.Run("a one-time token is refused if its use can't be recorded", func(t *testing.T) {
		dir := t.TempDir()
		path := filepath.Join(dir, "tokens.yaml")
		m, _ := NewManager(path)
		raw, _ := m.Add("once", nil, true)
		if err := os.Chmod(dir, 0o500); err != nil {
			t.Fatal(err)
		}
		defer os.Chmod(dir, 0o700) //nolint:errcheck
		if os.Geteuid() == 0 {
			t.Skip("root ignores directory permissions")
		}
		if _, err := m.Validate(raw); err == nil {
			t.Fatal("one-time token accepted without recording its use")
		}
	})
}
