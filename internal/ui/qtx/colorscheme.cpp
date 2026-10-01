#include <QGuiApplication>
#include <QStyleHints>
#include "colorscheme.h"

extern "C" int atlas_color_scheme(void) {
	if (QGuiApplication::instance() == nullptr) {
		return 0;
	}
	return static_cast<int>(QGuiApplication::styleHints()->colorScheme());
}
