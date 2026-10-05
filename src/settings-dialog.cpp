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
	QObject::connect(alt, &QCheckBox::toggled, signer_port, &QWidget::setEnabled);

	auto *port = new QSpinBox();
	port->setRange(1, 65535);
	port->setValue(cfg.port);

	form->addRow("TikTok username", user);
	form->addRow(alt);
	form->addRow("TikTok API key", key);
	form->addRow(auto_conn);
	form->addRow("Bridge port", port);
	form->addRow("Sign server port", signer_port);
	outer->addLayout(form);

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
