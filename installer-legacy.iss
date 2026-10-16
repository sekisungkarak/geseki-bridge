; Inno Setup script for Geseki Bridge (OBS 30-32 Legacy).
;
; Produces geseki-bridge-<version>-windows-installer-legacy.exe, which installs
; the plugin into the legacy OBS folder:
;
;   <OBS>\obs-plugins\64bit\geseki-bridge.dll
;   <OBS>\obs-plugins\64bit\geseki-bridge-tiktok.exe
;   <OBS>\data\obs-plugins\geseki-bridge\locale\en-US.ini

#define MyAppName "Geseki Bridge (Legacy OBS 30-32)"
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
DefaultDirName={code:GetOBSDir}
DisableDirPage=no
DirExistsWarning=no
AllowNoIcons=yes
Compression=lzma2/ultra64
SolidCompression=yes
LZMAAlgorithm=1
WizardStyle=modern
OutputDir=package
OutputBaseFilename=geseki-bridge-{#MyAppVersion}-windows-installer-legacy
ArchitecturesAllowed=x64compatible
ArchitecturesInstallIn64BitMode=x64compatible
PrivilegesRequired=admin
PrivilegesRequiredOverridesAllowed=dialog

[Languages]
Name: "english"; MessagesFile: "compiler:Default.isl"

[Files]
Source: "package\OBS 30-32\obs-plugins\*"; DestDir: "{app}\obs-plugins"; Flags: ignoreversion recursesubdirs createallsubdirs
Source: "package\OBS 30-32\data\*";        DestDir: "{app}\data";        Flags: ignoreversion recursesubdirs createallsubdirs

[Icons]
Name: "{group}\Uninstall {#MyAppName}"; Filename: "{uninstallexe}"

[Code]
function GetOBSDir(Param: string): string;
var
  InstallPath: string;
begin
  Result := ExpandConstant('{autopf}\obs-studio');
  if RegQueryStringValue(HKLM64, 'SOFTWARE\OBS Studio', '', InstallPath) then
    Result := InstallPath
  else if RegQueryStringValue(HKLM, 'SOFTWARE\OBS Studio', '', InstallPath) then
    Result := InstallPath;
end;
