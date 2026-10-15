package main

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"
	"text/tabwriter"

	"github.com/adishM98/auth-vpn/internal/client"
	"github.com/spf13/cobra"
)

// ─── profile ─────────────────────────────────────────────────────────────────

func profileCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "profile",
		Short: "Manage saved deployments (server, token, fingerprint) to connect by name",
	}
	cmd.AddCommand(profileAddCmd(), profileListCmd(), profileRemoveCmd())
	return cmd
}

func profileAddCmd() *cobra.Command {
	var host, token, fingerprint string
	var routes, forwards []string
	cmd := &cobra.Command{
		Use:     "add <name> <host[:port]>",
		Aliases: []string{"save"},
		Short:   "Save a deployment as a named profile (re-running updates it)",
		Example: `  auth-vpn profile add stage 203.0.113.10 --token <token> --fingerprint SHA256:...
  auth-vpn profile add prod-db 198.51.100.7:7777 --token <token> --forward 15432:10.8.0.1:5432`,
		Args: cobra.RangeArgs(1, 2),
		RunE: func(cmd *cobra.Command, args []string) error {
			if len(args) == 2 {
				host = args[1] // --host is kept for the old `profile save` form
			}
			if _, err := parseForwardRules(forwards); err != nil {
				return err
			}
			p := client.Profile{Name: args[0], Host: host, Token: token,
				Fingerprint: fingerprint, Routes: routes, Forwards: forwards}
			// Re-running add on an existing profile changes only what was
			// passed, e.g. `profile add stage --token <new>` after a rotation.
			old, lookupErr := client.LoadProfile(args[0])
			existed := lookupErr == nil
			if existed {
				f := cmd.Flags()
				if p.Host == "" {
					p.Host = old.Host
				}
				if !f.Changed("token") {
					p.Token = old.Token
				}
				if !f.Changed("fingerprint") {
					p.Fingerprint = old.Fingerprint
				}
				if !f.Changed("route") {
					p.Routes = old.Routes
				}
				if !f.Changed("forward") {
					p.Forwards = old.Forwards
				}
			} else if !errors.Is(lookupErr, client.ErrProfileNotFound) {
				return lookupErr
			}
			if err := client.SaveProfile(p); err != nil {
				return err
			}
			p, _ = client.LoadProfile(args[0])
			verb := "saved"
			if existed {
				verb = "updated"
			}
			sudo := "sudo "
			if len(p.Forwards) > 0 {
				sudo = "" // proxy mode needs no root
			}
			fmt.Fprintf(cmd.OutOrStdout(), "%s profile %q (%s)\nconnect: %sauth-vpn connect %s\n", verb, p.Name, p.Host, sudo, p.Name)
			return nil
		},
	}
	cmd.Flags().StringVar(&host, "host", "", "Server address host:port (or pass it as the second argument)")
	cmd.Flags().StringVarP(&token, "token", "t", "", "Auth token (leave out if this machine's IP is whitelisted)")
	cmd.Flags().StringVar(&fingerprint, "fingerprint", "", "Server certificate fingerprint (SHA256:...), pins every connection")
	cmd.Flags().StringArrayVar(&routes, "route", nil, "Extra CIDR to route through the tunnel. Repeatable.")
	cmd.Flags().StringArrayVar(&forwards, "forward", nil, "Proxy-mode forward localPort:remoteHost:remotePort. Repeatable.")
	_ = cmd.Flags().MarkHidden("host")
	return cmd
}

func profileListCmd() *cobra.Command {
	return &cobra.Command{
		Use:     "list",
		Aliases: []string{"ls"},
		Short:   "List saved profiles",
		RunE: func(cmd *cobra.Command, args []string) error {
			profiles, err := client.ListProfiles()
			if err != nil {
				return err
			}
			if len(profiles) == 0 {
				fmt.Fprintln(cmd.OutOrStdout(), noProfilesHelp)
				return nil
			}
			printProfiles(cmd.OutOrStdout(), profiles, false)
			return nil
		},
	}
}

func profileRemoveCmd() *cobra.Command {
	return &cobra.Command{
		Use:     "rm <name>",
		Aliases: []string{"remove", "delete"},
		Short:   "Delete a saved profile",
		Args:    cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := client.RemoveProfile(args[0]); err != nil {
				return err
			}
			fmt.Fprintf(cmd.OutOrStdout(), "removed profile %q\n", args[0])
			return nil
		},
	}
}

const noProfilesHelp = `no saved profiles yet. Save each deployment once:
  auth-vpn profile add <name> <host:port> --token <token> --fingerprint <SHA256:...>
then connect with: sudo auth-vpn connect <name>   (or just: sudo auth-vpn connect)`

// runningTunnel returns the live tunnel recorded in tunnel.json, if any.
func runningTunnel() (client.TunnelMeta, bool) {
	m, err := client.ReadMeta()
	if err != nil || !client.IsProcessAlive(m.PID) {
		return client.TunnelMeta{}, false
	}
	return m, true
}

// tunnelLabel names a tunnel for messages: its profile, else its address.
func tunnelLabel(m client.TunnelMeta) string {
	if m.Profile != "" {
		return m.Profile
	}
	return m.ServerAddr
}

func printProfiles(out io.Writer, profiles []client.Profile, numbered bool) {
	live, up := runningTunnel()
	w := tabwriter.NewWriter(out, 0, 0, 2, ' ', 0)
	head := "NAME\tSERVER\tMODE\tCERTIFICATE\tSTATUS"
	if numbered {
		head = "  #\t" + head
	}
	fmt.Fprintln(w, head)
	for i, p := range profiles {
		mode := "tun"
		if len(p.Routes) > 0 {
			mode += fmt.Sprintf(" +%d route(s)", len(p.Routes))
		}
		if len(p.Forwards) > 0 {
			mode = fmt.Sprintf("proxy, %d forward(s)", len(p.Forwards))
		}
		cert := "ask on first connect"
		if client.IsPinned(p) {
			cert = "pinned"
		}
		status := ""
		if up && live.Profile == p.Name {
			status = "● connected"
		}
		row := fmt.Sprintf("%s\t%s\t%s\t%s\t%s", p.Name, p.Host, mode, cert, status)
		if numbered {
			row = fmt.Sprintf("  %d\t%s", i+1, row)
		}
		fmt.Fprintln(w, row)
	}
	w.Flush()
}

// pickProfile shows the saved deployments and asks which one to connect to.
// It never guesses when several exist: connecting to the wrong environment
// (prod instead of stage) is the mistake this screen is here to prevent.
func pickProfile(in io.Reader, out io.Writer, interactive bool) (client.Profile, error) {
	profiles, err := client.ListProfiles()
	if err != nil {
		return client.Profile{}, err
	}
	if len(profiles) == 0 {
		return client.Profile{}, errors.New(noProfilesHelp)
	}
	if !interactive {
		names := make([]string, len(profiles))
		for i, p := range profiles {
			names[i] = p.Name
		}
		return client.Profile{}, fmt.Errorf("no server given; name one: auth-vpn connect <%s>", strings.Join(names, "|"))
	}
	if len(profiles) == 1 {
		fmt.Fprintf(out, "connecting to %s (%s), the only saved profile\n", profiles[0].Name, profiles[0].Host)
		return profiles[0], nil
	}

	fmt.Fprintf(out, "\nSaved deployments\n\n")
	printProfiles(out, profiles, true)
	r := bufio.NewReader(in)
	for {
		fmt.Fprintf(out, "\nConnect to [1-%d or name]: ", len(profiles))
		line, err := r.ReadString('\n')
		choice := strings.TrimSpace(line)
		if choice != "" {
			if n, convErr := strconv.Atoi(choice); convErr == nil && n >= 1 && n <= len(profiles) {
				return profiles[n-1], nil
			}
			for _, p := range profiles {
				if p.Name == choice {
					return p, nil
				}
			}
			fmt.Fprintf(out, "no profile %q", choice)
		}
		if err != nil {
			return client.Profile{}, errors.New("no profile chosen")
		}
	}
}
