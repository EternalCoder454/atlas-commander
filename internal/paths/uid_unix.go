//go:build unix

package paths

import (
	"os"
	"strconv"
)

func uidSuffix() string { return strconv.Itoa(os.Getuid()) }
