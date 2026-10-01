#include <QAbstractItemView>
#include <QApplication>
#include <QComboBox>
#include <QEvent>
#include <QMenu>
#include "popups.h"

namespace {

// translucent clears a popup's square backdrop so the style sheet's rounded
// border is all that shows. The attribute only takes effect before the
// native window exists, so a popup already shown is left as it is.
void translucent(QWidget *w) {
	if (w->testAttribute(Qt::WA_WState_Created) || w->testAttribute(Qt::WA_TranslucentBackground)) {
		return;
	}
	w->setAttribute(Qt::WA_TranslucentBackground);
	// The shadow X11 compositors draw is the window's square shape.
	w->setWindowFlag(Qt::NoDropShadowWindowHint);
}

// PopupRounder catches every widget's first polish, which comes before it is
// first shown. A menu polishes itself just before it pops up; a dropdown's
// list lives in a separate popup window that Qt makes on demand, so it is made
// here, when the dropdown itself is polished, long before it opens.
class PopupRounder : public QObject {
public:
	using QObject::QObject;

protected:
	bool eventFilter(QObject *obj, QEvent *ev) override {
		if (ev->type() != QEvent::Polish) {
			return false;
		}
		if (auto *menu = qobject_cast<QMenu *>(obj)) {
			translucent(menu);
		} else if (auto *combo = qobject_cast<QComboBox *>(obj)) {
			if (QWidget *popup = combo->view()->parentWidget()) {
				translucent(popup);
			}
		}
		return false;
	}
};

}  // namespace

extern "C" void atlas_round_popups(void) {
	if (QApplication::instance() == nullptr) {
		return;
	}
	static PopupRounder *rounder = nullptr;
	if (rounder == nullptr) {
		rounder = new PopupRounder(QApplication::instance());
		QApplication::instance()->installEventFilter(rounder);
	}
}
