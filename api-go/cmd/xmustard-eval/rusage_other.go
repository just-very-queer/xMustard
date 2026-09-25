//go:build !unix

package main

func selfMaxRSSBytes() int64 { return 0 }
