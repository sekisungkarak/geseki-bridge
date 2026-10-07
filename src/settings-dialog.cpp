/*
 * Geseki Bridge - native settings dialog (Qt6).
 *
 * The Tools menu opens this dialog. It edits the TikTok username, the fallback
 * signer, auto-connect and the bridge port, then hands them to
 * bridge-server's SaveConfig(), which persists them and applies the TikTok
 * settings live.
 *
 * The dialog mirrors what the sidecar actually does: room data comes from a
 * signature-free endpoint that needs nothing installed, so the signer below is
 * only a fallback for when that primary path fails.
 */
#include "settings-dialog.hpp"

#include "bridge-server.hpp"

#include <QCheckBox>
#include <QDialog>
#include <QDialogButtonBox>
#include <QFormLayout>
#include <QGroupBox>
#include <QLabel>
#include <QLineEdit>
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

	// Primary path: the sidecar fetches room data from a signature-free
	// endpoint, so connecting needs nothing installed.
	auto *primary = new QGroupBox("TikTok connection");
	auto *primary_form = new QFormLayout(primary);
	primary_form->setFieldGrowthPolicy(QFormLayout::AllNonFixedFieldsGrow);

	auto *user = new QLineEdit(QString::fromStdString(cfg.tiktok_username));
	user->setPlaceholderText("username (without @)");
	primary_form->addRow("TikTok username", user);

	auto *auto_conn = new QCheckBox("Connect automatically when OBS starts");
	auto_conn->setChecked(cfg.tiktok_autoconnect);
	primary_form->addRow(auto_conn);
	outer->addWidget(primary);

	// Fallback path, used only when the primary one fails. The checkbox picks
	// which signer it is: Euler Stream when on, the local one when off.
	auto *fallback = new QGroupBox("Fallback signer (optional)");
	auto *fallback_form = new QFormLayout(fallback);
	fallback_form->setFieldGrowthPolicy(QFormLayout::AllNonFixedFieldsGrow);

	auto *alt = new QCheckBox(
		"Use Euler Stream instead of the local sign server");
	alt->setChecked(cfg.alt_connection);
	alt->setToolTip(
		"Used only when the primary connection to TikTok fails. On: sign "
		"through Euler Stream. Off: sign through the local sign server.");
	fallback_form->addRow(alt);

	auto *key = new QLineEdit(QString::fromStdString(cfg.tiktok_api_key));
	key->setEchoMode(QLineEdit::Password);
	key->setPlaceholderText("optional");
	key->setToolTip(
		"Raises the Euler Stream rate limit. Only used when the fallback "
		"above is set to Euler Stream.");
	fallback_form->addRow("TikTok API key", key);

	auto *signer_port = new QSpinBox();
	signer_port->setRange(1, 65535);
	signer_port->setValue(cfg.sign_server_port);
	signer_port->setToolTip(
		"Port of the local sign server. Only used when the fallback above is "
		"set to the local sign server, and needs Node.js on PATH.");
	fallback_form->addRow("Local sign server port", signer_port);

	// The API key and the sign server belong to opposite fallbacks, so each is
	// editable only while its own fallback is selected.
	key->setEnabled(cfg.alt_connection);
	signer_port->setEnabled(!cfg.alt_connection);
	QObject::connect(alt, &QCheckBox::toggled, key, &QWidget::setEnabled);
	// signer_port is the opposite of the API key: it belongs to the local
	// fallback, so it must be enabled when Euler is NOT selected. Connecting
	// toggled() straight to setEnabled() would invert that, leaving the port
	// locked exactly when the local signer needs it.
	QObject::connect(alt, &QCheckBox::toggled, signer_port,
					 [signer_port](bool alt_on) { signer_port->setEnabled(!alt_on); });
	outer->addWidget(fallback);

	auto *bridge_form = new QFormLayout();
	bridge_form->setFieldGrowthPolicy(QFormLayout::AllNonFixedFieldsGrow);
	auto *port = new QSpinBox();
	port->setRange(1, 65535);
	port->setValue(cfg.port);
	bridge_form->addRow("Bridge port", port);
	outer->addLayout(bridge_form);

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
