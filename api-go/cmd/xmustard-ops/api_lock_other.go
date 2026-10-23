//go:build !unix

package main

// Without flock and process identity, no owner record can be validated: fail closed,
// so status never names an owner it cannot verify.
func apiOwnerMatches(string, int, string) bool { return false }

func matchingAPIOwner(string, int, string) (apiOwner, bool) { return apiOwner{}, false }
