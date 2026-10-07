/*
 * Geseki Bridge - native settings dialog (Qt6).
 *
 * The Tools menu opens this dialog. It edits the TikTok username, the optional
 * API key, auto-connect and the bridge port, then hands them to
 * bridge-server's SaveConfig(), which persists them and applies the TikTok
 * settings live.
 *
 * The dialog mirrors what the sidecar actually does: room data comes from a
 * signature-free endpoint that needs nothing installed, so the API key below
 * is only a fallback for when that primary path fails.
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

	auto *key = new QLineEdit(QString::fromStdString(cfg.tiktok_api_key));
	key->setEchoMode(QLineEdit::Password);
	key->setPlaceholderText("optional");
	key->setToolTip(
		"Used only when the primary connection to TikTok fails. Raises the "
		"rate limit of the signing service the sidecar falls back to.");
	primary_form->addRow("API key (optional)", key);

	auto *auto_conn = new QCheckBox("Connect automatically when OBS starts");
	auto_conn->setChecked(cfg.tiktok_autoconnect);
	primary_form->addRow(auto_conn);
	outer->addWidget(primary);

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
	next.tiktok_autoconnect = auto_conn->isChecked();
	next.port = port->value();
	// alt_connection and sign_server_port keep their stored values: the
	// config fields stay for compatibility, they are just no longer edited.
	geseki::bridge::SaveConfig(next);
}

} // namespace geseki::ui
