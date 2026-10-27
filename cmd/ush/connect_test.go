package main

import (
	"slices"
	"strings"
	"testing"
	"time"

	"ush/internal/config"
)

func TestConnectTakesATarget(t *testing.T) {
	for _, args := range [][]string{
		{"connect", "api", "--profile", "work"},
		{"connect", "--profile", "work", "api"},
		{"--profile", "work", "connect", "api"},
	} {
		o, err := parseArgs(args, env(nil))
		if err != nil {
			t.Fatalf("%v: %v", args, err)
		}
		if o.share || o.target != "api" || o.profile != "work" {
			t.Errorf("%v: got %+v", args, o)
		}
	}
}

func TestConnectTakesAMachine(t *testing.T) {
	for _, args := range [][]string{
		{"connect", "web", "-m", "nas"},
		{"connect", "--machine", "nas", "web"},
	} {
		o, err := parseArgs(args, env(nil))
		if err != nil || o.target != "web" || o.machine != "nas" {
			t.Errorf("%v: got %+v, %v", args, o, err)
		}
	}
	if _, err := parseArgs([]string{"connect", "-m", "nas"}, env(nil)); err == nil {
		t.Error("-m without a name should be refused")
	}
	if _, err := parseArgs([]string{"-m", "nas"}, env(nil)); err == nil {
		t.Error("-m is only for ush connect")
	}
}

func TestConnectWithoutATargetIsTheMenu(t *testing.T) {
	o, err := parseArgs([]string{"connect"}, env(map[string]string{"USH_PROFILE": "home"}))
	if err != nil || o.share || o.target != "" || o.profile != "home" {
		t.Fatalf("got %+v, %v", o, err)
	}
}

func TestConnectRefusesASecondTarget(t *testing.T) {
	if _, err := parseArgs([]string{"connect", "api", "web"}, env(nil)); err == nil {
		t.Fatal("two targets should be refused")
	}
	// A target is only for connect: bare ush still points at ush share.
	if _, err := parseArgs([]string{"api"}, env(nil)); err == nil {
		t.Fatal("bare ush NAME should still be refused")
	}
}

var live = []listedSession{
	{ID: "AAAAAAAAAAAAAAAAAAAA", Name: "api", Machine: "mac", User: "alex"},
	{ID: "BBBBBBBBBBBBBBBBBBBB", Name: "web", Machine: "mac", User: "alex"},
	{ID: "CCCCCCCCCCCCCCCCCCCC", Name: "web", Machine: "nas", User: "admin"},
}

func TestFindSessionByName(t *testing.T) {
	id, err := findSession(live, "api", "")
	if err != nil || id != "AAAAAAAAAAAAAAAAAAAA" {
		t.Fatalf("got %q, %v", id, err)
	}
}

func TestFindSessionByID(t *testing.T) {
	id, err := findSession(live, "CCCCCCCCCCCCCCCCCCCC", "")
	if err != nil || id != "CCCCCCCCCCCCCCCCCCCC" {
		t.Fatalf("got %q, %v", id, err)
	}
}

func TestFindSessionAmbiguousNameListsIDs(t *testing.T) {
	_, err := findSession(live, "web", "")
	if err == nil {
		t.Fatal("two sessions named web should be refused")
	}
	for _, want := range []string{"BBBBBBBBBBBBBBBBBBBB", "CCCCCCCCCCCCCCCCCCCC", "nas", "-m"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q should mention %s", err, want)
		}
	}
}

func TestFindSessionOnAMachine(t *testing.T) {
	id, err := findSession(live, "web", "NAS")
	if err != nil || id != "CCCCCCCCCCCCCCCCCCCC" {
		t.Fatalf("got %q, %v", id, err)
	}
	_, err = findSession(live, "api", "nas")
	if err == nil || !strings.Contains(err.Error(), `on "nas"`) || !strings.Contains(err.Error(), "web") {
		t.Fatalf("error %v should name the machine and what is live on it", err)
	}
}

func TestFindSessionTwinsOnOneMachineSkipMachineHint(t *testing.T) {
	twins := []listedSession{
		{ID: "AAAAAAAAAAAAAAAAAAAA", Name: "api", Machine: "mac"},
		{ID: "BBBBBBBBBBBBBBBBBBBB", Name: "api", Machine: "mac"},
	}
	_, err := findSession(twins, "api", "mac")
	if err == nil || strings.Contains(err.Error(), "-m") {
		t.Fatalf("error %v: -m cannot tell these apart, so should not be suggested", err)
	}
}

func TestFindSessionMissingNamesWhatIsLive(t *testing.T) {
	_, err := findSession(live, "db", "")
	if err == nil || !strings.Contains(err.Error(), "api, web") {
		t.Fatalf("error %v should list the live names once each", err)
	}
	_, err = findSession(nil, "db", "")
	if err == nil || !strings.Contains(err.Error(), "no sessions are live") {
		t.Fatalf("error %v should say nothing is live", err)
	}
}

func TestBrowseArgsCarryARemoteCommand(t *testing.T) {
	cfg := config.Config{Rendezvous: "rv.example.net", Port: "7874", User: "ush", Key: "/k", KnownHosts: "/kh"}
	args := browseArgs(cfg, "join", "AAAAAAAAAAAAAAAAAAAA")
	n := len(args)
	if args[n-3] != "ush@rv.example.net" || args[n-2] != "join" || args[n-1] != "AAAAAAAAAAAAAAAAAAAA" {
		t.Fatalf("want destination then join ID, got %v", args)
	}
}

func TestConnectList(t *testing.T) {
	o, err := parseArgs([]string{"connect", "--list", "-m", "nas"}, env(nil))
	if err != nil || !o.list || o.machine != "nas" || o.target != "" {
		t.Fatalf("got %+v, %v", o, err)
	}
	if _, err := parseArgs([]string{"connect", "--list", "api"}, env(nil)); err == nil {
		t.Error("--list with a name should be refused")
	}
	if _, err := parseArgs([]string{"--list"}, env(nil)); err == nil {
		t.Error("--list is only for ush connect")
	}
}

func TestPrintSessions(t *testing.T) {
	now := time.Unix(100000, 0)
	sessions := []listedSession{
		{ID: "CCCCCCCCCCCCCCCCCCCC", Name: "web", Machine: "nas", User: "admin", StartedEpoch: now.Add(-15 * time.Minute).Unix()},
		{ID: "AAAAAAAAAAAAAAAAAAAA", Name: "api", Machine: "mac", User: "alex", StartedEpoch: now.Add(-2*time.Hour - 5*time.Minute).Unix()},
		{ID: "BBBBBBBBBBBBBBBBBBBB", Name: "web", Machine: "mac", User: "alex", StartedEpoch: now.Add(-3 * 24 * time.Hour).Unix()},
	}
	var out strings.Builder
	printSessions(&out, sessions, "", now)
	lines := strings.Split(strings.TrimSpace(out.String()), "\n")
	if len(lines) != 4 || !strings.HasPrefix(lines[0], "NAME") {
		t.Fatalf("want a header and three rows, got:\n%s", out.String())
	}
	for i, want := range [][]string{
		{"api", "mac", "alex", "2h", "AAAAAAAAAAAAAAAAAAAA"},
		{"web", "mac", "alex", "3d", "BBBBBBBBBBBBBBBBBBBB"},
		{"web", "nas", "admin", "15m", "CCCCCCCCCCCCCCCCCCCC"},
	} {
		if got := strings.Fields(lines[i+1]); !slices.Equal(got, want) {
			t.Errorf("row %d: got %v, want %v", i, got, want)
		}
	}

	out.Reset()
	printSessions(&out, sessions, "NAS", now)
	if s := out.String(); strings.Contains(s, "mac") || !strings.Contains(s, "CCCCCCCCCCCCCCCCCCCC") {
		t.Errorf("-m nas should keep only nas:\n%s", s)
	}

	out.Reset()
	printSessions(&out, nil, "", now)
	if out.String() != "no sessions are live\n" {
		t.Errorf("empty: got %q", out.String())
	}
	out.Reset()
	printSessions(&out, sessions, "elsewhere", now)
	if out.String() != "no sessions are live on \"elsewhere\"\n" {
		t.Errorf("empty machine: got %q", out.String())
	}
}
