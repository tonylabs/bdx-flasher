package main

import (
	"os"
	"os/exec"
	"syscall"
)

// Jetson boards are flashed by the existing BDX Orin Flasher, which handles
// NVIDIA's recovery-mode initrd flash, firmware download and clean install.
// This app only selects the board and hands over.

func orinFlasherPath() string {
	for _, name := range []string{"bdx-orin-flasher", "bdx-orin-beta-flasher"} {
		if p, err := exec.LookPath(name); err == nil {
			return p
		}
	}
	return ""
}

type jetsonOpts struct {
	ManifestURL string
	LocalBundle string
	LocalSHA256 string
	LocalEnv    string
	EraseAll    bool
	Extra       []string // further flags passed through verbatim
}

func (o jetsonOpts) args() []string {
	var a []string
	if o.ManifestURL != "" {
		a = append(a, "--manifest-url", o.ManifestURL)
	}
	if o.LocalBundle != "" {
		a = append(a, "--local-bundle", o.LocalBundle)
		if o.LocalSHA256 != "" {
			a = append(a, "--local-sha256", o.LocalSHA256)
		}
		if o.LocalEnv != "" {
			a = append(a, "--local-env", o.LocalEnv)
		}
	}
	if o.EraseAll {
		a = append(a, "--erase-all")
	}
	return append(a, o.Extra...)
}

// launchOrinFlasherGUI starts the Orin flasher's own window and returns.
func launchOrinFlasherGUI(o jetsonOpts) error {
	cmd := exec.Command(orinFlasherPath(), o.args()...)
	cmd.Env = append(os.Environ(), "RESOURCE_NAME=org.openbdx.bdx-orin-flasher")
	if err := cmd.Start(); err != nil {
		return err
	}
	go cmd.Wait()
	return nil
}

// execOrinFlasherCLI replaces this process with the Orin flasher in terminal mode.
func execOrinFlasherCLI(o jetsonOpts) error {
	path := orinFlasherPath()
	argv := append([]string{path, "--cli"}, o.args()...)
	return syscall.Exec(path, argv, os.Environ())
}
