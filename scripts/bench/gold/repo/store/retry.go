package store

import "time"

// RetryWithBackoff calls fn until it succeeds, doubling the delay after each failure.
func RetryWithBackoff(attempts int, base time.Duration, fn func() error) error {
	var err error
	delay := base
	for i := 0; i < attempts; i++ {
		if err = fn(); err == nil {
			return nil
		}
		time.Sleep(delay)
		delay *= 2
	}
	return err
}
