package main

// apiOwner is the identity a running API records beside its admission lock.
type apiOwner struct {
	PID       int    `json:"pid"`
	StartTime int64  `json:"start_time"`
	DataDir   string `json:"data_dir"`
	Port      int    `json:"port"`
}
