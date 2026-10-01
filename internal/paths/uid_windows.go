package paths

import "os"

// Windows' temp directory is already per user (%LOCALAPPDATA%\Temp), so the
// suffix only needs to be stable.
func uidSuffix() string { return os.Getenv("USERNAME") }
