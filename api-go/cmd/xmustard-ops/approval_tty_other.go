//go:build !darwin && !linux

package main

import "errors"

// readTerminalSecret has no echo-free prompt on this platform.
func readTerminalSecret(string) (string, error) {
	return "", errors.New("no terminal prompt on this platform; pass --token-file or XMUSTARD_APPROVER_TOKEN, which are advisory")
}
