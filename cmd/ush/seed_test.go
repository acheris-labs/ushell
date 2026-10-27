package main

import (
	"bytes"
	"strings"
	"testing"

	"ush/internal/psk"
)

func noEnv(string) string { return "" }

func TestSeedFromEnvironment(t *testing.T) {
	s, _ := psk.NewSeed()
	got, err := readSeed(initArgs{}, strings.NewReader(""), &bytes.Buffer{},
		func(k string) string { return map[string]string{"USH_SEED": s}[k] })
	if err != nil || got != s {
		t.Fatalf("got %q err=%v", got, err)
	}
}

func TestSeedPasted(t *testing.T) {
	s, _ := psk.NewSeed()
	var out bytes.Buffer
	got, err := readSeed(initArgs{}, strings.NewReader(strings.ToUpper(s)+"\n"), &out, noEnv)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := psk.ParseSeed(got); err != nil {
		t.Fatalf("returned seed does not parse: %v", err)
	}
	if !strings.Contains(out.String(), "Seed") {
		t.Fatalf("expected a prompt, got %q", out.String())
	}
}

func TestSeedEmptyAnswerIsAnError(t *testing.T) {
	// It used to generate a new seed, which silently produced the wrong key
	// when someone meant to paste one.
	_, err := readSeed(initArgs{}, strings.NewReader("\n"), &bytes.Buffer{}, noEnv)
	if err == nil || !strings.Contains(err.Error(), "USH_SEED") {
		t.Fatalf("want an error pointing at USH_SEED, got %v", err)
	}
}

func TestSeedYesWithoutEnvIsAnError(t *testing.T) {
	_, err := readSeed(initArgs{yes: true}, strings.NewReader(""), &bytes.Buffer{}, noEnv)
	if err == nil {
		t.Fatal("-y with no $USH_SEED must fail, not invent a seed")
	}
}

func TestSeedConfirmsWhatWasRead(t *testing.T) {
	s, _ := psk.NewSeed()
	var out bytes.Buffer
	if _, err := readSeed(initArgs{}, strings.NewReader(s+"\n"), &out, noEnv); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "seed read (52 characters)") {
		t.Fatalf("want a confirmation, got %q", out.String())
	}
}

func TestSeedInvalid(t *testing.T) {
	_, err := readSeed(initArgs{}, strings.NewReader("nope\n"), &bytes.Buffer{}, noEnv)
	if err == nil || !strings.Contains(err.Error(), "seed") {
		t.Fatalf("want a seed error, got %v", err)
	}
}
