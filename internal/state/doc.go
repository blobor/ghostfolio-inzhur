//go:build unix

// Package state stores the Inzhur refresh token between runs.
//
// The refresh token changes on each refresh. The package writes the new
// token to a file in the data directory immediately.
//
// The package uses flock, thus it builds only on Unix systems.
package state
