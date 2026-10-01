package qtx

/*
#cgo pkg-config: Qt6Widgets
#include "popups.h"
*/
import "C"

// RoundPopups makes every menu and dropdown list from now on paint without a
// square backdrop, so the corners the style sheet rounds look rounded. Qt
// style sheets cannot set this, and MIQT cannot override eventFilter without
// a call into Go for every event in the program, so it is done in C++.
// Call it only where the window itself can be translucent: without a
// compositor the cleared corners show black, as the window's own do.
func RoundPopups() { C.atlas_round_popups() }
