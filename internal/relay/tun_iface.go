package relay

import "io"

// TUNIface is the common interface for TUN/TAP devices on all platforms.
type TUNIface interface {
	io.ReadWriteCloser
	Name() string
}
