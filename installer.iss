; Inno Setup script for Geseki Bridge.
;
; Produces geseki-bridge-<version>-windows-installer.exe, which installs the
; plugin straight into the OBS folder:
;
;   <OBS>\obs-plugins\64bit\geseki-bridge.dll
;   <OBS>\obs-plugins\64bit\geseki-bridge-tiktok.exe
;   <OBS>\data\obs-plugins\geseki-bridge\locale\en-US.ini
;
; The default target is auto-detected:
;   * installer OBS  -> HKLM\SOFTWARE\OBS Studio (the official setup writes it)
;   * otherwise      -> C:\Program Files\obs-studio
; The user can always browse to a portable OBS folder instead.
;
; The file tree under "package\" is the flat OBS layout produced by
; cmake --install (obs-plugins/ + data/), so it is copied verbatim into {app}.

#define MyAppName "Geseki Bridge"
#define MyAppVersion GetEnv("GESEKI_VERSION")
#if MyAppVersion == ""
  #define MyAppVersion "0.0.0"
#endif
#define MyAppPublisher "Sekisungkarak"
#define MyAppURL "https://github.com/sekisungkarak/geseki-bridge"

[Setup]
AppId={{7B4E1C2A-5D3F-4A91-9E7C-2F8A6B0D3E51}
AppName={#MyAppName}
AppVersion={#MyAppVersion}
AppPublisher={#MyAppPublisher}
AppPublisherURL={#MyAppURL}
AppSupportURL={#MyAppURL}
VersionInfoVersion={#MyAppVersion}
VersionInfoCompany={#MyAppPublisher}
VersionInfoDescription={#MyAppName} Setup
; No fixed DefaultDirName: it is chosen at runtime by GetOBSDir() below, which
; reads the OBS install location so the plugin lands in the right tree.
DefaultDirName={code:GetOBSDir}
DisableDirPage=no
DirExistsWarning=no
AllowNoIcons=yes
Compression=lzma2/ultra64
SolidCompression=yes
LZMAAlgorithm=1
WizardStyle=modern
OutputDir=package
OutputBaseFilename=geseki-bridge-{#MyAppVersion}-windows-installer
; OBS is 64-bit only on Windows; install into the 64-bit Program Files view.
ArchitecturesAllowed=x64compatible
ArchitecturesInstallIn64BitMode=x64compatible
; Default target is Program Files\obs-studio, so request elevation by default;
; the dialog override lets a user pick "only for me" if they want to.
PrivilegesRequired=admin
PrivilegesRequiredOverridesAllowed=dialog

[Languages]
Name: "english"; MessagesFile: "compiler:Default.isl"

[Files]
; Flat OBS tree: obs-plugins/ + data/ copied verbatim into the OBS folder.
Source: "package\obs-plugins\*"; DestDir: "{app}\obs-plugins"; Flags: ignoreversion recursesubdirs createallsubdirs
Source: "package\data\*";        DestDir: "{app}\data";        Flags: ignoreversion recursesubdirs createallsubdirs

[Icons]
Name: "{group}\Uninstall {#MyAppName}"; Filename: "{uninstallexe}"

[Code]
function GetOBSDir(Param: string): string;
var
  InstallPath: string;
begin
  // Default to the standard installer location...
  Result := ExpandConstant('{autopf}\obs-studio');
  // ...but prefer whatever OBS Studio recorded in the registry at setup time.
  if RegQueryStringValue(HKLM64, 'SOFTWARE\OBS Studio', '', InstallPath) then
    Result := InstallPath
  else if RegQueryStringValue(HKLM, 'SOFTWARE\OBS Studio', '', InstallPath) then
    Result := InstallPath;
end;
