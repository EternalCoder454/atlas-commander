package qtx

import (
	"os"
	"unsafe"

	"golang.org/x/sys/windows"
)

var spi = windows.NewLazySystemDLL("user32.dll").NewProc("SystemParametersInfoW")

const spiGetClientAreaAnimation = 0x1042

// AnimationsEnabled reports whether Windows' "Animation effects" setting is
// on. Someone who has turned animations off is not shown one at startup.
func AnimationsEnabled() bool {
	if os.Getenv("ATLAS_NO_ANIMATIONS") != "" {
		return false
	}
	var on int32 = 1
	if r, _, _ := spi.Call(spiGetClientAreaAnimation, 0, uintptr(unsafe.Pointer(&on)), 0); r == 0 {
		return true
	}
	return on != 0
}
