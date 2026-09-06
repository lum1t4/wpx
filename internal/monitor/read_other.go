//go:build !linux

package monitor

import "errors"

// ReadSystem is deliberately unsupported away from the Linux production host.
// A development preview reports this limit instead of plausible-looking zeros.
func ReadSystem(_ []string) (Raw, error) {
	return Raw{}, errors.New("local resource monitoring is only available on Linux")
}
