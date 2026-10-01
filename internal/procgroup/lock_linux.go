package procgroup

import "runtime"

func lockThread() { runtime.LockOSThread() }
