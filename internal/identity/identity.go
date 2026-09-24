// Package identity describes who uses the pool and on which machines.
// A person is independent of both their devices and the accounts they share.
package identity

type Person struct {
	ID          string
	DisplayName string
}

type Platform string

const (
	PlatformDarwin  Platform = "darwin"
	PlatformWindows Platform = "windows"
)

type Device struct {
	ID       string
	PersonID string
	Name     string
	Platform Platform
}
