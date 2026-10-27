package main

import (
	"os"
	"strings"
	"testing"
)

func TestEnsureLoader(t *testing.T) {
	if os.Getenv("NET_TESTS") == "" {
		t.Skip("NET_TESTS not set")
	}
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	b, _ := boardBySlug("rock5bplus")
	p, err := ensureLoader(b, "")
	if err != nil {
		t.Fatal(err)
	}
	if sum, _ := fileSHA256(p); sum != b.Loader.SHA256 {
		t.Fatalf("cached loader hash %s", sum)
	}
	b.Loader.SHA256 = strings.Repeat("0", 64)
	os.Remove(p)
	if _, err := ensureLoader(b, ""); err == nil || !strings.Contains(err.Error(), "expected") {
		t.Fatalf("tampered loader accepted: %v", err)
	}
}

func TestParsers(t *testing.T) {
	m := listRE.FindStringSubmatch("DevNo=1\tVid=0x2207,Pid=0x350b,LocationID=104\tMaskrom")
	if m == nil || m[1] != "2207" || m[2] != "350b" || m[3] != "Maskrom" {
		t.Fatalf("list parse: %v", m)
	}
	if m := flashSizeRE.FindStringSubmatch("Flash Info:\n\tManufacturer: SAMSUNG, value=00\n\tFlash Size: 59640 MB\n"); m == nil || m[1] != "59640" {
		t.Fatalf("flash size parse: %v", m)
	}
	if m := pctRE.FindStringSubmatch("Write LBA from file (42%)"); m == nil || m[1] != "42" {
		t.Fatalf("progress parse: %v", m)
	}
}
