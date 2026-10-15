package main

import (
	"bytes"
	"strings"
	"testing"

	"github.com/adishM98/auth-vpn/internal/client"
)

func TestPickProfile(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	t.Run("no saved profiles explains how to add one", func(t *testing.T) {
		_, err := pickProfile(strings.NewReader(""), &bytes.Buffer{}, true)
		if err == nil || !strings.Contains(err.Error(), "auth-vpn profile add") {
			t.Fatalf("err = %v", err)
		}
	})

	_ = client.SaveProfile(client.Profile{Name: "stage", Host: "203.0.113.10", Token: "s"})

	t.Run("a single profile is used without asking", func(t *testing.T) {
		p, err := pickProfile(strings.NewReader(""), &bytes.Buffer{}, true)
		if err != nil || p.Name != "stage" {
			t.Fatalf("got %q, %v", p.Name, err)
		}
	})

	_ = client.SaveProfile(client.Profile{Name: "prod", Host: "198.51.100.7", Token: "p",
		Fingerprint: "SHA256:x", Forwards: []string{"15432:10.8.0.1:5432"}})

	for _, tc := range []struct{ name, input, want string }{
		{"picks by number", "2\n", "prod"},
		{"picks by name", "stage\n", "stage"},
		{"asks again after a wrong answer", "9\nnope\n1\n", "stage"},
		{"accepts a last line without newline", "2", "prod"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var out bytes.Buffer
			p, err := pickProfile(strings.NewReader(tc.input), &out, true)
			if err != nil || p.Name != tc.want {
				t.Fatalf("got %q, %v", p.Name, err)
			}
		})
	}

	t.Run("menu shows server, mode and certificate per deployment", func(t *testing.T) {
		var out bytes.Buffer
		_, _ = pickProfile(strings.NewReader("1\n"), &out, true)
		for _, want := range []string{"203.0.113.10:7777", "ask on first connect", "198.51.100.7:7777", "proxy, 1 forward(s)", "pinned"} {
			if !strings.Contains(out.String(), want) {
				t.Errorf("menu missing %q:\n%s", want, out.String())
			}
		}
	})

	t.Run("ending input without a choice connects nowhere", func(t *testing.T) {
		if _, err := pickProfile(strings.NewReader("\n"), &bytes.Buffer{}, true); err == nil {
			t.Fatal("empty answer picked a profile")
		}
	})

	t.Run("without a terminal it never guesses and lists the names", func(t *testing.T) {
		_, err := pickProfile(strings.NewReader("1\n"), &bytes.Buffer{}, false)
		if err == nil || !strings.Contains(err.Error(), "stage|prod") {
			t.Fatalf("err = %v", err)
		}
	})
}

func TestOfferToSaveProfile(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	_ = client.SaveProfile(client.Profile{Name: "stage", Host: "203.0.113.10:7777", Token: "s"})

	for _, tc := range []struct{ name, addr, input, want string }{
		{"Enter skips", "198.51.100.7:7777", "\n", ""},
		{"end of input skips", "198.51.100.7:7777", "", ""},
		{"takes a name", "198.51.100.7:7777", "prod\n", "prod"},
		{"re-asks on a taken or invalid name", "198.51.100.7:7777", "stage\nbad name\nprod\n", "prod"},
		{"never asks for a server that already has a profile", "203.0.113.10:7777", "x\n", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := askProfileName(strings.NewReader(tc.input), &bytes.Buffer{}, tc.addr); got != tc.want {
				t.Fatalf("got %q, want %q", got, tc.want)
			}
		})
	}

	t.Run("saves once, after connecting, with the trusted fingerprint", func(t *testing.T) {
		_ = client.TrustFingerprint("198.51.100.7:7777", "SHA256:prod")
		save := saveProfileOnce(client.Profile{Name: "prod", Host: "198.51.100.7:7777", Token: "p"})
		if _, err := client.LoadProfile("prod"); err == nil {
			t.Fatal("saved before the tunnel came up")
		}
		save()
		save() // a reconnect must not save or print again
		p, err := client.LoadProfile("prod")
		if err != nil || p.Token != "p" || p.Fingerprint != "SHA256:prod" {
			t.Fatalf("got %+v, %v", p, err)
		}
	})
}
