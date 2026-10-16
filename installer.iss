; Inno Setup script for Geseki Bridge (OBS 33+).
;
; Produces geseki-bridge-<version>-windows-installer.exe, which installs
; the plugin into the modern OBS 33+ plugins folder:
;
;   %ProgramData%\obs-studio\plugins\geseki-bridge\geseki-bridge.dll
;   %ProgramData%\obs-studio\plugins\geseki-bridge\geseki-bridge-tiktok.exe
;   %ProgramData%\obs-studio\plugins\geseki-bridge\data\locale\en-US.ini
;   %ProgramData%\obs-studio\plugins\geseki-bridge\data\manifest.json

#define MyAppName "Geseki Bridge"
#define MyAppVersion GetEnv("GESEKI_VERSION")
#if MyAppVersion == ""
  #define MyAppVersion "0.0.0"
#endif
#define MyAppPublisher "Sekisungkarak"
#define MyAppURL "https://github.com/sekisungkarak/geseki-bridge"

[Setup]
AppId={{9C5F2D3B-6E4A-5B02-AF8D-3E9B7C1E4F62}
AppName={#MyAppName}
AppVersion={#MyAppVersion}
AppPublisher={#MyAppPublisher}
AppPublisherURL={#MyAppURL}
AppSupportURL={#MyAppURL}
VersionInfoVersion={#MyAppVersion}
VersionInfoCompany={#MyAppPublisher}
VersionInfoDescription={#MyAppName} Setup
DefaultDirName={commonappdata}\obs-studio\plugins\geseki-bridge
DisableDirPage=no
DirExistsWarning=no
AllowNoIcons=yes
Compression=lzma2/ultra64
SolidCompression=yes
LZMAAlgorithm=1
WizardStyle=modern
OutputDir=package
OutputBaseFilename=geseki-bridge-{#MyAppVersion}-windows-installer
ArchitecturesAllowed=x64compatible
ArchitecturesInstallIn64BitMode=x64compatible
PrivilegesRequired=admin
PrivilegesRequiredOverridesAllowed=dialog

[Languages]
Name: "english"; MessagesFile: "compiler:Default.isl"

[Files]
Source: "package\OBS 33\geseki-bridge\*"; DestDir: "{app}"; Flags: ignoreversion recursesubdirs createallsubdirs

[Icons]
Name: "{group}\Uninstall {#MyAppName}"; Filename: "{uninstallexe}"
