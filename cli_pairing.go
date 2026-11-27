package main

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/joeylipton/btctl/internal/bluez"
	"github.com/joeylipton/btctl/internal/desktop"
)

// cliPrefix tags btctl's own progress and prompt lines on stderr.
const cliPrefix = "[btctl] "

// cliPairing answers pairing requests for the command-line commands (connect,
// pair, and the scan prompt). The agent only consults it about the device the
// command is pairing, so confirmations are accepted; codes the user must type
// on the device are printed and raised as notifications; passkeys and PINs
// are read from the terminal when there is one.
type cliPairing struct {
	In          io.Reader // nil means os.Stdin
	Out         io.Writer // nil means os.Stderr
	Interactive bool      // In is a terminal the user can type into
}

func (h cliPairing) out() io.Writer {
	if h.Out == nil {
		return os.Stderr
	}
	return h.Out
}

// Confirm accepts the request and tells the user it did so.
func (h cliPairing) Confirm(_ context.Context, req bluez.PairingRequest) bool {
	if req.Kind == bluez.PairingConfirm {
		fmt.Fprintf(h.out(), "%sconfirming pairing with %s (passkey %06d)\n", cliPrefix, req.Address, req.Passkey)
	} else {
		fmt.Fprintf(h.out(), "%saccepting pairing with %s\n", cliPrefix, req.Address)
	}
	desktop.Notify("Auto-authorized connection from " + req.Address)
	return true
}

// Display prints the code once and raises a notification.
func (h cliPairing) Display(req bluez.PairingRequest) {
	if req.Entered > 0 {
		return // BlueZ repeats DisplayPasskey for every key typed
	}
	msg := fmt.Sprintf("Type %s on %s, then press Enter", req.Code(), req.Address)
	fmt.Fprintf(h.out(), "%s%s\n", cliPrefix, msg)
	desktop.Alert("btctl - pairing code", msg)
}

// Input reads a passkey or PIN from the terminal. Without one, a PIN request
// falls back to the conventional 0000 and a passkey request is rejected.
func (h cliPairing) Input(ctx context.Context, req bluez.PairingRequest) (string, bool) {
	if !h.Interactive {
		if req.Kind == bluez.PairingEnterPinCode {
			fmt.Fprintf(h.out(), "%s%s asked for a PIN; trying %s\n", cliPrefix, req.Address, bluez.DefaultPinCode)
			return bluez.DefaultPinCode, true
		}
		fmt.Fprintf(h.out(), "%s%s needs the passkey shown on it, but there is no terminal to type it into; use btctl pick\n", cliPrefix, req.Address)
		return "", false
	}
	what := "the 6-digit passkey shown on"
	if req.Kind == bluez.PairingEnterPinCode {
		what = "the PIN for (often " + bluez.DefaultPinCode + ")"
	}
	fmt.Fprintf(h.out(), "%sEnter %s %s: ", cliPrefix, what, req.Address)
	in := h.In
	if in == nil {
		in = os.Stdin
	}
	lineCh := make(chan string, 1)
	go func() {
		line, _ := bufio.NewReader(in).ReadString('\n')
		lineCh <- line
	}()
	select {
	case line := <-lineCh:
		return strings.TrimSpace(line), true
	case <-ctx.Done():
		fmt.Fprintln(h.out())
		return "", false
	}
}
