/*
 * Geseki Bridge — native backup dialog (Qt6).
 *
 * Opened from Tools > Geseki > Backup. Three panels:
 *
 *   Automatic Backups      a snapshot on OBS start, at most once a day, and how
 *                          many to keep
 *   Where Backups Are Saved the folder (default: the plugin's own config folder)
 *                          with Open Folder / Use Default / Choose Folder...
 *   Manual Backup          Back Up Now and Restore From Backup...
 *
 * The dialog is a thin shell: all file work lives in backup.cpp, so the same
 * engine also serves the automatic snapshot taken at plugin load.
 */
#include "backup-dialog.hpp"

#include "backup.hpp"

#include <QCheckBox>
#include <QDesktopServices>
#include <QDialog>
#include <QDialogButtonBox>
#include <QFileDialog>
#include <QFont>
#include <QFontDatabase>
#include <QHBoxLayout>
#include <QLabel>
#include <QListWidget>
#include <QMessageBox>
#include <QPushButton>
#include <QSpinBox>
#include <QStringList>
#include <QUrl>
#include <QVBoxLayout>
#include <QWidget>

namespace geseki::ui {

namespace {

QLabel *SectionTitle(const QString &text)
{
	auto *l = new QLabel(text);
	QFont f = l->font();
	f.setBold(true);
	f.setPointSizeF(f.pointSizeF() + 0.5);
	l->setFont(f);
	return l;
}

QLabel *SectionBody(const QString &text)
{
	auto *l = new QLabel(text);
	l->setWordWrap(true);
	l->setEnabled(false); // dimmed, like a description
	return l;
}

// "backup-20261005-094153-manual" -> "2026-10-05 09:41:53   manual   (109 KB)"
QString PrettyEntry(const geseki::backup::Entry &e)
{
	QString rest = QString::fromStdString(e.name);
	if (rest.startsWith("backup-"))
		rest = rest.mid(7);

	QString when, label;
	const QStringList parts = rest.split('-');
	if (parts.size() >= 3 && parts[0].size() == 8 && parts[1].size() == 6) {
		const QString d = parts[0];
		const QString t = parts[1];
		when = d.mid(0, 4) + "-" + d.mid(4, 2) + "-" + d.mid(6, 2) + " " + t.mid(0, 2) +
		       ":" + t.mid(2, 2) + ":" + t.mid(4, 2);
		label = parts.mid(2).join("-");
	} else {
		when = rest;
	}

	const long long kb = e.size / 1024;
	return when + "    " + label + "    (" + QString::number(kb) + " KB)";
}

} // namespace

void ShowBackupDialog(void *parent)
{
	geseki::backup::Settings settings = geseki::backup::GetSettings();

	QDialog dlg(static_cast<QWidget *>(parent));
	dlg.setWindowTitle("Geseki Backup");
	dlg.setMinimumWidth(560);

	auto *outer = new QVBoxLayout(&dlg);
	outer->setSpacing(14);

	// ── Automatic Backups ────────────────────────────────────────────────
	outer->addWidget(SectionTitle("Automatic Backups"));
	outer->addWidget(SectionBody(
		"Saves a backup when OBS starts, at most once a day, so a day of work is "
		"never more than one restore away. It includes the bridge settings and every "
		"widget's settings, since they stay on this machine."));

	auto *auto_row = new QHBoxLayout();
	auto *auto_box = new QCheckBox("Back up automatically");
	auto_box->setChecked(settings.auto_enabled);
	auto_row->addWidget(auto_box);
	auto_row->addStretch(1);
	auto_row->addWidget(new QLabel("Backups to keep"));
	auto *keep = new QSpinBox();
	keep->setRange(1, 100);
	keep->setValue(settings.keep);
	auto_row->addWidget(keep);
	outer->addLayout(auto_row);

	// ── Where Backups Are Saved ──────────────────────────────────────────
	outer->addWidget(SectionTitle("Where Backups Are Saved"));
	outer->addWidget(SectionBody(
		"By default backups sit beside the bridge config inside OBS, so they travel "
		"with a portable install. Choose your own folder to keep them on another drive "
		"or somewhere that syncs to the cloud."));

	auto *path = new QLabel();
	path->setTextInteractionFlags(Qt::TextSelectableByMouse);
	path->setWordWrap(true);
	path->setFont(QFontDatabase::systemFont(QFontDatabase::FixedFont));
	outer->addWidget(path);

	auto *path_row = new QHBoxLayout();
	auto *open_btn = new QPushButton("Open Folder");
	auto *default_btn = new QPushButton("Use Default");
	auto *choose_btn = new QPushButton("Choose Folder...");
	path_row->addStretch(1);
	path_row->addWidget(open_btn);
	path_row->addWidget(default_btn);
	path_row->addWidget(choose_btn);
	outer->addLayout(path_row);

	auto refresh_path = [&] {
		const bool is_default = settings.dir.empty();
		path->setText(QString::fromStdString(geseki::backup::EffectiveDir()));
		default_btn->setEnabled(!is_default);
	};
	refresh_path();

	// ── Manual Backup ────────────────────────────────────────────────────
	outer->addWidget(SectionTitle("Manual Backup"));

	auto *manual_row = new QHBoxLayout();
	auto *restore_btn = new QPushButton("Restore From Backup...");
	auto *now_btn = new QPushButton("Back Up Now");
	now_btn->setDefault(true);
	manual_row->addStretch(1);
	manual_row->addWidget(restore_btn);
	manual_row->addWidget(now_btn);
	outer->addLayout(manual_row);

	auto *buttons = new QDialogButtonBox(QDialogButtonBox::Close);
	QObject::connect(buttons, &QDialogButtonBox::rejected, &dlg, &QDialog::reject);
	outer->addWidget(buttons);

	// ── Wiring ───────────────────────────────────────────────────────────

	QObject::connect(open_btn, &QPushButton::clicked, &dlg, [&] {
		QDesktopServices::openUrl(
			QUrl::fromLocalFile(QString::fromStdString(geseki::backup::EffectiveDir())));
	});

	QObject::connect(default_btn, &QPushButton::clicked, &dlg, [&] {
		settings.dir.clear();
		refresh_path();
	});

	QObject::connect(choose_btn, &QPushButton::clicked, &dlg, [&] {
		const QString dir = QFileDialog::getExistingDirectory(
			&dlg, "Choose backup folder",
			QString::fromStdString(geseki::backup::EffectiveDir()));
		if (dir.isEmpty())
			return;
		settings.dir = dir.toStdString();
		refresh_path();
	});

	QObject::connect(now_btn, &QPushButton::clicked, &dlg, [&] {
		// Persist the folder choice first, so the snapshot lands where the path
		// label says it will.
		settings.auto_enabled = auto_box->isChecked();
		settings.keep = keep->value();
		geseki::backup::SaveSettings(settings);

		std::string error;
		const std::string name = geseki::backup::CreateNow("manual", error);
		if (name.empty()) {
			QMessageBox::warning(&dlg, "Backup failed",
					     QString::fromStdString(error.empty()
								    ? "could not write the backup"
								    : error));
			return;
		}
		QMessageBox::information(
			&dlg, "Backup created",
			QString("Saved to:\n%1")
				.arg(QString::fromStdString(
					geseki::backup::EffectiveDir() + "\\" + name)));
	});

	QObject::connect(restore_btn, &QPushButton::clicked, &dlg, [&] {
		const std::vector<geseki::backup::Entry> entries = geseki::backup::List();
		if (entries.empty()) {
			QMessageBox::information(&dlg, "Restore From Backup",
						 "There is no backup yet. Use \"Back Up Now\" first.");
			return;
		}

		QDialog pick(&dlg);
		pick.setWindowTitle("Restore From Backup");
		pick.setMinimumWidth(460);
		auto *lay = new QVBoxLayout(&pick);
		lay->addWidget(new QLabel("Choose a backup to restore:"));
		auto *list = new QListWidget();
		for (const auto &e : entries)
			list->addItem(PrettyEntry(e));
		list->setCurrentRow(0);
		lay->addWidget(list);

		auto *pick_buttons =
			new QDialogButtonBox(QDialogButtonBox::Ok | QDialogButtonBox::Cancel);
		QObject::connect(pick_buttons, &QDialogButtonBox::accepted, &pick, &QDialog::accept);
		QObject::connect(pick_buttons, &QDialogButtonBox::rejected, &pick, &QDialog::reject);
		lay->addWidget(pick_buttons);

		if (pick.exec() != QDialog::Accepted)
			return;
		const int row = list->currentRow();
		if (row < 0 || row >= static_cast<int>(entries.size()))
			return;
		const std::string name = entries[static_cast<size_t>(row)].name;

		if (QMessageBox::question(
			    &dlg, "Restore From Backup",
			    QString("Restore \"%1\" over the current settings?")
				    .arg(QString::fromStdString(name)),
			    QMessageBox::Yes | QMessageBox::No, QMessageBox::No) != QMessageBox::Yes)
			return;

		std::string report, error;
		if (!geseki::backup::Restore(name, report, error)) {
			QMessageBox::warning(&dlg, "Restore failed",
					     QString::fromStdString(error));
			return;
		}
		QMessageBox::information(
			&dlg, "Restore complete",
			QString::fromStdString(report) +
				"\n\nRestart OBS so the restored settings take effect. If some "
				"widget settings did not come back, close OBS completely and "
				"restore again — the browser storage is locked while OBS runs.");
	});

	// ── Save on close ────────────────────────────────────────────────────
	if (dlg.exec() == QDialog::Accepted) {
		// The Close button rejects; this branch is a safety net if a future
		// button accepts.
	}
	settings.auto_enabled = auto_box->isChecked();
	settings.keep = keep->value();
	geseki::backup::SaveSettings(settings);
}

} // namespace geseki::ui
