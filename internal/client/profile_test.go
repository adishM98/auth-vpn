package client

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func TestProfileStore(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	t.Run("add fills the default port and round-trips every field", func(t *testing.T) {
		in := Profile{Name: "stage", Host: "203.0.113.10", Token: "tok", Fingerprint: "SHA256:abc",
			Routes: []string{"10.20.0.0/16"}, Forwards: []string{"15432:10.8.0.1:5432"}}
		if err := SaveProfile(in); err != nil {
			t.Fatal(err)
		}
		got, err := LoadProfile("stage")
		want := in
		want.Host = "203.0.113.10:7777"
		if err != nil || !reflect.DeepEqual(got, want) {
			t.Fatalf("got %+v, %v; want %+v", got, err, want)
		}
	})

	t.Run("saving an existing name replaces it instead of duplicating", func(t *testing.T) {
		_ = SaveProfile(Profile{Name: "prod", Host: "198.51.100.7:7777", Token: "a"})
		_ = SaveProfile(Profile{Name: "prod", Host: "198.51.100.7:7777", Token: "b"})
		all, _ := ListProfiles()
		if len(all) != 2 || all[1].Token != "b" {
			t.Fatalf("got %+v", all)
		}
	})

	t.Run("remove deletes one profile and reports unknown names", func(t *testing.T) {
		if err := RemoveProfile("prod"); err != nil {
			t.Fatal(err)
		}
		if err := RemoveProfile("prod"); !errors.Is(err, ErrProfileNotFound) {
			t.Fatalf("second remove: %v", err)
		}
		all, _ := ListProfiles()
		if len(all) != 1 || all[0].Name != "stage" {
			t.Fatalf("got %+v", all)
		}
	})

	t.Run("file is private to the user", func(t *testing.T) {
		fi, err := os.Stat(profilesPath())
		if err != nil || fi.Mode().Perm() != 0o600 {
			t.Fatalf("mode %v, %v", fi.Mode(), err)
		}
	})

	t.Run("a corrupt file is an error, so saving never wipes it", func(t *testing.T) {
		_ = os.WriteFile(profilesPath(), []byte("profiles: [broken"), 0o600)
		if err := SaveProfile(Profile{Name: "x", Host: "h:1"}); err == nil {
			t.Fatal("save over a corrupt file succeeded")
		}
		data, _ := os.ReadFile(profilesPath())
		if string(data) != "profiles: [broken" {
			t.Fatal("corrupt file was overwritten")
		}
	})
}

func TestNormalizeProfileRejectsBadNames(t *testing.T) {
	for _, name := range []string{"", "a b", "host:7777", "a/b"} {
		if _, err := NormalizeProfile(Profile{Name: name, Host: "h"}); err == nil {
			t.Errorf("name %q accepted", name)
		}
	}
	if _, err := NormalizeProfile(Profile{Name: "ok"}); err == nil {
		t.Error("profile without host accepted")
	}
	p, _ := NormalizeProfile(Profile{Name: "v6", Host: "[2001:db8::1]"})
	if p.Host != "[2001:db8::1]:7777" {
		t.Errorf("ipv6 host = %q", p.Host)
	}
}

func TestClearMetaLeavesAnotherTunnelsState(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	// The tunnel that took over after a switch owns tunnel.json; the old
	// process shutting down must not delete it.
	_ = writePrivate(metaFile(), []byte(`{"pid":999999,"profile":"prod"}`))
	if err := ClearMeta(); err != nil {
		t.Fatal(err)
	}
	if m, err := ReadMeta(); err != nil || m.Profile != "prod" {
		t.Fatalf("state removed: %+v %v", m, err)
	}
	if err := WriteMeta(TunnelMeta{Profile: "stage"}); err != nil {
		t.Fatal(err)
	}
	_ = ClearMeta()
	if _, err := os.Stat(metaFile()); !os.IsNotExist(err) {
		t.Fatal("own state not removed")
	}
}

func TestIsInteractiveFalseForDevNull(t *testing.T) {
	f, err := os.Open(os.DevNull)
	if err != nil {
		t.Skip(err)
	}
	defer f.Close()
	old := os.Stdin
	os.Stdin = f
	defer func() { os.Stdin = old }()
	if IsInteractive() {
		t.Fatal("/dev/null stdin treated as a terminal")
	}
}

func TestWritePrivateRefusesSymlinkedDir(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	_ = os.Symlink(t.TempDir(), filepath.Join(home, ".auth-vpn"))
	if err := writePrivate(metaFile(), []byte("{}")); err == nil {
		t.Fatal("wrote through a symlinked state dir")
	}
}
