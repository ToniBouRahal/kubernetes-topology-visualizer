package commands

import (
	"bytes"
	"context"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/fyp/kubernetes-topology-visualizer/cli/internal/api"
	"github.com/fyp/kubernetes-topology-visualizer/cli/internal/auth"
)

// D-15.5: `mcp` serves the five tools on its transport, asks Env.Client for a client on every
// call, prints nothing to stdout itself, and returns 0 when the agent hangs up.
func TestMCPSubcommandServesTheTools(t *testing.T) {
	fake := newFakeAPI(t, fixtures)
	serverT, clientT := mcp.NewInMemoryTransports()
	var out, errOut bytes.Buffer
	var clients atomic.Int32
	env := Env{
		Stdout: &out, Stderr: &errOut,
		Now:          func() time.Time { return now },
		MCPTransport: serverT,
		Client: func(_ context.Context, o ClientOptions) (*api.Client, error) {
			clients.Add(1)
			return api.New(o.Server, http.DefaultClient, staticToken(testToken))
		},
	}
	done := make(chan int, 1)
	go func() {
		code, _ := Run(context.Background(), env, "mcp", []string{"--server", fake.srv.URL})
		done <- code
	}()

	cs, err := mcp.NewClient(&mcp.Implementation{Name: "test-agent", Version: "0"}, nil).Connect(context.Background(), clientT, nil)
	if err != nil {
		t.Fatal(err)
	}
	tools, err := cs.ListTools(context.Background(), nil)
	if err != nil || len(tools.Tools) != 5 {
		t.Fatalf("ListTools: %v, %d tools", err, len(tools.Tools))
	}
	for range 2 {
		res, err := cs.CallTool(context.Background(), &mcp.CallToolParams{Name: "list_namespaces"})
		if err != nil || res.IsError {
			t.Fatalf("list_namespaces: %v %+v", err, res)
		}
		if text := res.Content[0].(*mcp.TextContent).Text; !strings.Contains(text, "demo") {
			t.Errorf("list_namespaces: %q", text)
		}
	}
	_ = cs.Close()
	select {
	case code := <-done:
		if code != ExitOK {
			t.Errorf("exit %d, stderr %q", code, errOut.String())
		}
	case <-time.After(10 * time.Second):
		t.Fatal("mcp did not return after the client hung up")
	}
	if n := clients.Load(); n != 2 {
		t.Errorf("Env.Client called %d times, want once per tool call", n)
	}
	if out.Len() != 0 {
		t.Errorf("mcp wrote to stdout: %q", out.String())
	}
}

func TestMCPSubcommandUsageErrorsGoToStderr(t *testing.T) {
	var out, errOut bytes.Buffer
	env := Env{Stdout: &out, Stderr: &errOut}
	if code, _ := Run(context.Background(), env, "mcp", []string{"extra"}); code != ExitUsage {
		t.Errorf("exit %d, want %d", code, ExitUsage)
	}
	if out.Len() != 0 || errOut.Len() == 0 {
		t.Errorf("stdout %q, stderr %q", out.String(), errOut.String())
	}
}

type notSignedIn struct{ server string }

func (n notSignedIn) Token(context.Context) (string, error) {
	return "", &auth.NotSignedInError{Server: n.server}
}

// auth's NotSignedInError already names the login command; it is printed once, not wrapped in
// a second "not signed in … run login".
func TestNotSignedInIsPrintedOnce(t *testing.T) {
	fake := newFakeAPI(t, fixtures)
	r := run(t, fake.srv.URL, notSignedIn{fake.srv.URL}, "namespaces")
	if r.code != ExitAuth || strings.Count(r.stderr, "kubectl topology login") != 1 {
		t.Errorf("exit %d, stderr %q", r.code, r.stderr)
	}
}
