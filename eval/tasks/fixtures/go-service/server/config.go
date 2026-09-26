// Package server holds the service's listener configuration.
package server

// DefaultPort is the TCP port the service listens on when none is configured.
const DefaultPort = 8080

// Addr returns the listen address for port p (DefaultPort when p is 0).
func Addr(p int) string {
	if p == 0 {
		p = DefaultPort
	}
	return ":" + itoa(p)
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b [20]byte
	i := len(b)
	for n > 0 {
		i--
		b[i] = byte('0' + n%10)
		n /= 10
	}
	return string(b[i:])
}
