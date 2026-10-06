/*
 * Geseki Bridge — native settings dialog (Qt6).
 *
 * The Tools menu opens this dialog. It edits the TikTok username, the sign
 * server port, auto-connect, the bridge port and the Alternative Connection
 * Mode, then hands them to bridge-server's SaveConfig(), which persists them
 * and applies the TikTok settings live.
 */
#include "settings-dialog.hpp"

#include "bridge-server.hpp"

#include <QCheckBox>
#include <QDialog>
#include <QDialogButtonBox>
#include <QFormLayout>
#include <QHBoxLayout>
#include <QLabel>
#include <QLineEdit>
#include <QPushButton>
#include <QSpinBox>
#include <QVBoxLayout>
#include <QWidget>

namespace geseki::ui {

void ShowSettingsDialog(void *parent)
{
	const geseki::bridge::Config cfg = geseki::bridge::GetConfig();

	QDialog dlg(static_cast<QWidget *>(parent));
	dlg.setWindowTitle("Geseki Bridge");
	dlg.setMinimumWidth(460);

	auto *outer = new QVBoxLayout(&dlg);

	auto *intro = new QLabel(
		"Connect OBS to a TikTok LIVE room and to Windows media sessions.");
	intro->setWordWrap(true);
	outer->addWidget(intro);

	auto *form = new QFormLayout();
	form->setFieldGrowthPolicy(QFormLayout::AllNonFixedFieldsGrow);

	auto *user = new QLineEdit(QString::fromStdString(cfg.tiktok_username));
	user->setPlaceholderText("username (without @)");

	auto *alt = new QCheckBox("Alternative Connection Mode");
	alt->setChecked(cfg.alt_connection);
	alt->setToolTip(
		"Sign TikTok requests through a remote service instead of the local "
		"sign server. Use it when Node.js or Google Chrome is unavailable. "
		"The service applies a shared rate limit, which an API key raises.");

	auto *key = new QLineEdit(QString::fromStdString(cfg.tiktok_api_key));
	key->setEchoMode(QLineEdit::Password);
	key->setPlaceholderText("optional");
	key->setToolTip("Only used by the Alternative Connection Mode.");

	auto *auto_conn = new QCheckBox("Connect automatically when OBS starts");
	auto_conn->setChecked(cfg.tiktok_autoconnect);

	auto *signer_port = new QSpinBox();
	signer_port->setRange(1, 65535);
	signer_port->setValue(cfg.sign_server_port);
	signer_port->setToolTip(
		"Port of the local sign server. TikTok is signed locally, so it needs "
		"Node.js on PATH and Google Chrome installed.");

	// The API key and the sign server belong to opposite modes, so each is
	// editable only while its own mode is selected.
	key->setEnabled(cfg.alt_connection);
	signer_port->setEnabled(!cfg.alt_connection);
	QObject::connect(alt, &QCheckBox::toggled, key, &QWidget::setEnabled);
	// signer_port is the opposite of the API key: it belongs to the local
	// mode, so it must be enabled when Alternative Connection Mode is OFF.
	// Connecting toggled() straight to setEnabled() would invert that (the
	// signal carries the new checked state), leaving the port locked exactly
	// when the local signer needs it.
	QObject::connect(alt, &QCheckBox::toggled, signer_port,
			 [signer_port](bool alt_on) { signer_port->setEnabled(!alt_on); });

	// TikTok refuses the live chat endpoint unless the signer's browser profile
	// holds a real session, and that failure looks like a signature fault from
	// the outside. Show the state and offer the fix right here, so nobody has
	// to find a README and run Node by hand.
	auto *sign_in_row = new QWidget();
	auto *sign_in_box = new QHBoxLayout(sign_in_row);
	sign_in_box->setContentsMargins(0, 0, 0, 0);

	auto *sign_in_state = new QLabel();
	sign_in_state->setWordWrap(true);
	sign_in_box->addWidget(sign_in_state, 1);

	auto *sign_in_btn = new QPushButton("Sign in to TikTok");
	sign_in_btn->setToolTip(
		"Opens Chrome so you can sign in to TikTok. The local signer needs a "
		"signed-in session; without one the live chat connection is refused.");
	sign_in_box->addWidget(sign_in_btn, 0);

	// The signer runs only in local mode, so this is meaningless (and the button
	// is pointless) while the alternative mode signs remotely.
	const bool local_mode = !cfg.alt_connection;
	if (!local_mode) {
		sign_in_state->setText("Not used in Alternative Connection Mode.");
		sign_in_btn->setEnabled(false);
	} else if (geseki::bridge::SignerSignedIn(cfg.sign_server_port)) {
		sign_in_state->setText("Signed in to TikTok.");
	} else {
		sign_in_state->setText(
			"Not signed in. TikTok refuses the live chat connection until you sign in.");
	}

	QObject::connect(sign_in_btn, &QPushButton::clicked, &dlg,
			 [signer_port, sign_in_state, sign_in_btn] {
				 if (geseki::bridge::StartSignInFlow(signer_port->value())) {
					 sign_in_state->setText(
						 "Chrome opened. Sign in there; the sign server "
						 "restarts automatically once you are done.");
					 sign_in_btn->setEnabled(false);
				 } else {
					 sign_in_state->setText(
						 "Could not open Chrome. Check that Node.js and "
						 "Google Chrome are installed.");
				 }
			 });

	auto *port = new QSpinBox();
	port->setRange(1, 65535);
	port->setValue(cfg.port);

	form->addRow("TikTok username", user);
	form->addRow(alt);
	form->addRow("TikTok API key", key);
	form->addRow(auto_conn);
	form->addRow("Bridge port", port);
	form->addRow("Sign server port", signer_port);
	form->addRow("TikTok sign-in", sign_in_row);
	outer->addLayout(form);

	// Toggling the mode must also flip the sign-in row, not just the port field.
	QObject::connect(alt, &QCheckBox::toggled, &dlg,
			 [sign_in_btn, sign_in_state](bool alt_on) {
				 sign_in_btn->setEnabled(!alt_on);
				 if (alt_on)
					 sign_in_state->setText(
						 "Not used in Alternative Connection Mode.");
			 });

	auto *hint = new QLabel(
		QString("Widgets connect to ws://127.0.0.1:%1/ws. "
			"A port change takes effect after restarting OBS.")
			.arg(cfg.port));
	hint->setWordWrap(true);
	outer->addWidget(hint);

	auto *buttons = new QDialogButtonBox(
		QDialogButtonBox::Save | QDialogButtonBox::Cancel);

	QObject::connect(buttons, &QDialogButtonBox::accepted, &dlg,
			 &QDialog::accept);
	QObject::connect(buttons, &QDialogButtonBox::rejected, &dlg,
			 &QDialog::reject);
	outer->addWidget(buttons);

	if (dlg.exec() != QDialog::Accepted)
		return;

	geseki::bridge::Config next = cfg;
	next.tiktok_username = user->text().trimmed().toStdString();
	next.tiktok_api_key = key->text().toStdString();
	next.alt_connection = alt->isChecked();
	next.tiktok_autoconnect = auto_conn->isChecked();
	next.port = port->value();
	next.sign_server_port = signer_port->value();
	geseki::bridge::SaveConfig(next);
}

} // namespace geseki::ui
