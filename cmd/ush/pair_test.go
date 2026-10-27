package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"ush/internal/config"
	"ush/internal/psk"
)

func pairVector(t *testing.T) (seed string, payload string) {
	t.Helper()
	b, err := os.ReadFile("../../test/vectors/pair.json")
	if err != nil {
		t.Fatal(err)
	}
	var v struct{ Seed, Payload string }
	if err := json.Unmarshal(b, &v); err != nil {
		t.Fatal(err)
	}
	return v.Seed, v.Payload
}

func vectorProfile(t *testing.T) config.Config {
	t.Helper()
	seed, _ := pairVector(t)
	b, _ := psk.ParseSeed(seed)
	key := filepath.Join(t.TempDir(), "key")
	if _, err := psk.Write(key, psk.FromSeed(b)); err != nil {
		t.Fatal(err)
	}
	return config.Config{Profile: "home", Rendezvous: "192.168.10.5", Port: "7874", User: "ush", Key: key}
}

func TestPairPayloadIsTheVector(t *testing.T) {
	_, want := pairVector(t)
	got, err := pairPayload(vectorProfile(t))
	if err != nil {
		t.Fatal(err)
	}
	if got != want {
		t.Fatalf("payload\n got %s\nwant %s", got, want)
	}
}

func TestPairRefusesOffATerminal(t *testing.T) {
	f, err := os.CreateTemp(t.TempDir(), "out")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	err = pair(vectorProfile(t), f, f)
	if err == nil || !strings.Contains(err.Error(), "terminal") {
		t.Fatalf("want a refusal naming the terminal, got %v", err)
	}
	if st, _ := f.Stat(); st.Size() != 0 {
		t.Fatal("wrote to a non-terminal; the seed may have leaked")
	}
}

func TestPairRefusesAMissingKey(t *testing.T) {
	cfg := vectorProfile(t)
	cfg.Key = filepath.Join(t.TempDir(), "absent")
	if _, err := pairPayload(cfg); err == nil {
		t.Fatal("a missing key produced a payload")
	}
}
