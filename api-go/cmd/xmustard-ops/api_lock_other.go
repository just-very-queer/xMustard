//go:build !unix

package main

func apiOwnerMatches(string, int, string) bool { return false }
