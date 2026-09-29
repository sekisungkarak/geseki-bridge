/*
 * Geseki Bridge — native settings dialog (Qt6).
 *
 * The Tools menu opens this dialog. It edits the TikTok username, API key,
 * auto-connect and the bridge port, then hands them to bridge-server's
 * SaveConfig(), which persists them and applies the TikTok credentials live.
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

	auto *key = new QLineEdit(QString::fromStdString(cfg.tiktok_api_key));
	key->setEchoMode(QLineEdit::Password);
	key->setPlaceholderText("optional");

	auto *auto_conn = new QCheckBox("Connect automatically when OBS starts");
	auto_conn->setChecked(cfg.tiktok_autoconnect);

	auto *port = new QSpinBox();
	port->setRange(1, 65535);
	port->setValue(cfg.port);

	form->addRow("TikTok username", user);
	form->addRow("TikTok API key", key);
	form->addRow(auto_conn);
	form->addRow("Bridge port", port);
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
	next.tiktok_autoconnect = auto_conn->isChecked();
	next.port = port->value();
	geseki::bridge::SaveConfig(next);
}

} // namespace geseki::ui
