//go:build linux

package relay

import (
	"fmt"

	"github.com/songgao/water"
)

func createTUNIface(name, cidr string) (TUNIface, error) {
	iface, err := water.New(water.Config{
		DeviceType: water.TUN,
		PlatformSpecificParams: water.PlatformSpecificParams{
			Name: name,
		},
	})
	if err != nil {
		return nil, fmt.Errorf("create TUN %s: %w", name, err)
	}
	return iface, nil
}
