package auth

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/url"
	"os/exec"
	"runtime"
	"slices"
	"strings"
	"time"
)

// Exit codes, D-15.4. Duplicated as plain numbers in the read commands; these three subcommands
// only use the ones below.
const (
	exitOK         = 0
	exitUsage      = 1
	exitNotSigned  = 2
	exitOther      = 3
	exitConnection = 4
)

// Commands carries what login, logout and status need; tests replace the browser, the store and
// the clock. The zero value is not usable: see NewCommands.
type Commands struct {
	Stdout, Stderr io.Writer
	Store          *Store
	OpenBrowser    func(url string) error
	Now            func() time.Time

	// Ports and CallbackTimeout default to CallbackPorts and CallbackTimeout.
	Ports           []int
	CallbackTimeout time.Duration
}

// NewCommands wires the default store and the system browser.
func NewCommands(stdout, stderr io.Writer) (*Commands, error) {
	store, err := DefaultStore()
	if err != nil {
		return nil, err
	}
	return &Commands{Stdout: stdout, Stderr: stderr, Store: store, OpenBrowser: openBrowser, Now: time.Now}, nil
}

// Login is `kubectl topology login`.
func Login(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	return run(stdout, stderr, func(c *Commands) int { return c.Login(ctx, args) })
}

// Logout is `kubectl topology logout`.
func Logout(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	return run(stdout, stderr, func(c *Commands) int { return c.Logout(ctx, args) })
}

// Status is `kubectl topology status`.
func Status(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	return run(stdout, stderr, func(c *Commands) int { return c.Status(ctx, args) })
}

func run(stdout, stderr io.Writer, f func(*Commands) int) int {
	c, err := NewCommands(stdout, stderr)
	if err != nil {
		fmt.Fprintln(stderr, "error:", err)
		return exitOther
	}
	return f(c)
}

func (c *Commands) flags(name, usage string) *flag.FlagSet {
	fs := flag.NewFlagSet(name, flag.ContinueOnError)
	fs.SetOutput(c.Stderr)
	fs.Usage = func() {
		fmt.Fprintf(c.Stderr, "Usage: kubectl topology %s\n\n", usage)
		fs.PrintDefaults()
	}
	return fs
}

func (c *Commands) parse(fs *flag.FlagSet, args []string) (ok bool, code int) {
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return false, exitOK
		}
		return false, exitUsage
	}
	if fs.NArg() > 0 {
		fmt.Fprintf(c.Stderr, "error: unexpected argument %q\n", fs.Arg(0))
		fs.Usage()
		return false, exitUsage
	}
	return true, exitOK
}

// Login signs in through the browser (D-15.8). There is no flag that disables TLS verification;
// --ca-file adds a CA instead (D-15.14).
func (c *Commands) Login(ctx context.Context, args []string) int {
	fs := c.flags("login", "login [--server URL] [--issuer URL] [--client-id ID] [--scopes S] [--ca-file PEM]")
	server := fs.String("server", DefaultServer, "topology UI URL; must equal the chart's auth.externalUrl")
	issuer := fs.String("issuer", "", "OIDC issuer URL (default <server>/dex, the bundled Dex)")
	clientID := fs.String("client-id", DefaultClientID, "OIDC public client ID")
	scopes := fs.String("scopes", "", "scopes to request, space- or comma-separated (default \"openid email profile offline_access\", plus groups when the provider supports it)")
	caFile := fs.String("ca-file", "", "PEM CA to trust for an https server or issuer with a private certificate")
	if ok, code := c.parse(fs, args); !ok {
		return code
	}
	// Flag values SignIn would only reject later are usage errors (exit 1), not "not signed in".
	if err := validateLoginFlags(*server, *issuer, *caFile); err != nil {
		fmt.Fprintln(c.Stderr, "error:", err)
		return exitUsage
	}

	res, err := SignIn(ctx, c.Store, SignInOptions{
		Server:      *server,
		Issuer:      *issuer,
		ClientID:    *clientID,
		Scopes:      splitScopes(*scopes),
		CAFile:      *caFile,
		Ports:       c.Ports,
		Timeout:     c.CallbackTimeout,
		OpenBrowser: c.OpenBrowser,
		Prompt:      c.Stderr,
	})
	if err != nil {
		fmt.Fprintln(c.Stderr, "error:", err)
		var ce *ConnectionError
		switch {
		case errors.As(err, &ce):
			return exitConnection
		case errors.Is(err, ErrInsecureTransport):
			return exitUsage
		default:
			return exitNotSigned
		}
	}
	if res.NoAuth {
		fmt.Fprintf(c.Stdout, "%s does not require sign-in; nothing was stored.\n", res.Server)
		return exitOK
	}
	fmt.Fprintf(c.Stdout, "Signed in to %s as %s; the token expires at %s and refreshes automatically.\n",
		res.Server, orUnknown(res.Email), res.Expiry.Local().Format(time.RFC3339))
	return exitOK
}

// Logout deletes the local entry (D-15.12). Dex has no revocation endpoint, so a token already
// issued stays valid until it expires; the message says so.
func (c *Commands) Logout(ctx context.Context, args []string) int {
	fs := c.flags("logout", "logout [--server URL | --all]")
	server := fs.String("server", DefaultServer, "server to sign out of")
	all := fs.Bool("all", false, "sign out of every server")
	if ok, code := c.parse(fs, args); !ok {
		return code
	}
	norm, err := NormalizeServer(*server)
	if err != nil {
		fmt.Fprintln(c.Stderr, "error:", err)
		return exitUsage
	}
	var removed []string
	err = c.Store.Update(ctx, func(f *File) error {
		for s := range f.Servers {
			if *all || s == norm {
				removed = append(removed, s)
				delete(f.Servers, s)
			}
		}
		if len(removed) == 0 {
			return errUnchanged
		}
		return nil
	})
	if err != nil {
		fmt.Fprintln(c.Stderr, "error:", err)
		return exitOther
	}
	if len(removed) == 0 {
		if *all {
			fmt.Fprintln(c.Stdout, "Not signed in to any server.")
		} else {
			fmt.Fprintf(c.Stdout, "Not signed in to %s.\n", norm)
		}
		return exitOK
	}
	slices.Sort(removed)
	for _, s := range removed {
		fmt.Fprintf(c.Stdout, "Signed out of %s.\n", s)
	}
	fmt.Fprintln(c.Stdout, "Tokens already issued stay valid until they expire (the provider has no revocation).")
	return exitOK
}

// Status prints, per server, the server, the signed-in e-mail and the expiry, nothing more
// (D-15.11). It reads without refreshing.
func (c *Commands) Status(_ context.Context, args []string) int {
	fs := c.flags("status", "status [--server URL]")
	server := fs.String("server", "", "server to report (default: every stored server)")
	if ok, code := c.parse(fs, args); !ok {
		return code
	}
	f, err := c.Store.Load()
	if err != nil {
		fmt.Fprintln(c.Stderr, "error:", err)
		return exitOther
	}
	var servers []string
	if *server != "" {
		norm, err := NormalizeServer(*server)
		if err != nil {
			fmt.Fprintln(c.Stderr, "error:", err)
			return exitUsage
		}
		servers = []string{norm}
	} else {
		for s := range f.Servers {
			servers = append(servers, s)
		}
		slices.Sort(servers)
	}
	if len(servers) == 0 {
		fmt.Fprintln(c.Stdout, "Not signed in to any server.")
		return exitNotSigned
	}

	now := time.Now
	if c.Now != nil {
		now = c.Now
	}
	code := exitOK
	for i, s := range servers {
		if i > 0 {
			fmt.Fprintln(c.Stdout)
		}
		e := f.Servers[s]
		fmt.Fprintf(c.Stdout, "server:   %s\n", s)
		if !e.SignedIn() {
			fmt.Fprintf(c.Stdout, "status:   not signed in; run \"kubectl topology login --server %s\"\n", s)
			code = exitNotSigned
			continue
		}
		email := ""
		if cl, err := parseClaims(e.IDToken); err == nil {
			email = cl.Email
		}
		fmt.Fprintf(c.Stdout, "user:     %s\n", orUnknown(email))
		expiry := e.Expiry.Local().Format(time.RFC3339)
		switch remaining := e.Expiry.Sub(now()); {
		case remaining > 0:
			fmt.Fprintf(c.Stdout, "expires:  %s (in %s)\n", expiry, remaining.Round(time.Second))
		case e.RefreshToken != "":
			fmt.Fprintf(c.Stdout, "expires:  %s (expired; refreshes on next use)\n", expiry)
		default:
			fmt.Fprintf(c.Stdout, "expires:  %s (expired; run login again)\n", expiry)
			code = exitNotSigned
		}
	}
	return code
}

// validateLoginFlags checks the URLs' form and that the CA file is a readable PEM.
func validateLoginFlags(server, issuer, caFile string) error {
	if _, err := NormalizeServer(server); err != nil {
		return err
	}
	if issuer != "" {
		u, err := url.Parse(issuer)
		if err != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") {
			return fmt.Errorf("invalid issuer URL %q: want http(s)://host[:port][/path]", issuer)
		}
	}
	abs, err := absCAFile(caFile)
	if err != nil {
		return err
	}
	_, err = NewHTTPClient(abs)
	return err
}

func splitScopes(s string) []string {
	return strings.FieldsFunc(s, func(r rune) bool { return r == ',' || r == ' ' })
}

func orUnknown(s string) string {
	if s == "" {
		return "(no e-mail claim)"
	}
	return s
}

// openBrowser starts the platform's URL opener without waiting for it.
func openBrowser(url string) error {
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "darwin":
		cmd = exec.Command("open", url)
	default:
		cmd = exec.Command("xdg-open", url)
	}
	if err := cmd.Start(); err != nil {
		return err
	}
	go func() { _ = cmd.Wait() }()
	return nil
}
